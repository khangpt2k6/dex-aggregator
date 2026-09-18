package api

import (
	"context"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/graph"
)

// PriceTick is one price update pushed to a subscribed client.
type PriceTick struct {
	Pair  string `json:"pair"`
	Price string `json:"price"`
	Time  int64  `json:"time"`
}

// clientBuffer is how many ticks may queue for one connection.
//
// A client that falls this far behind is dropped rather than allowed to block
// the broadcaster. One slow browser on a bad connection must not be able to
// stall price updates for everyone else, and a tick it would receive seconds
// late is worthless anyway.
const clientBuffer = 32

type client struct {
	send chan PriceTick
	once sync.Once
	done chan struct{}
}

func (c *client) close() {
	c.once.Do(func() { close(c.done) })
}

// hub fans price ticks out to connected websocket clients.
type hub struct {
	holder *cache.Holder

	mu      sync.RWMutex
	clients map[*client]struct{}
}

func newHub(h *cache.Holder) *hub {
	return &hub{holder: h, clients: make(map[*client]struct{})}
}

func (h *hub) add(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	c.close()
}

// broadcast delivers a tick to every client that can keep up.
func (h *hub) broadcast(ticks []PriceTick) {
	h.mu.RLock()
	targets := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		for _, t := range ticks {
			select {
			case c.send <- t:
			default:
				// Buffer full. Drop the client instead of blocking here.
				c.close()
			}
		}
	}
}

// ClientCount reports how many websocket clients are connected.
func (h *hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Run pushes prices derived from the current snapshot until ctx is done.
func (h *hub) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			return
		case <-ticker.C:
			if ticks := h.currentPrices(); len(ticks) > 0 {
				h.broadcast(ticks)
			}
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.close()
	}
	h.clients = make(map[*client]struct{})
}

// trackedPairs are the pairs the stream publishes. Pushing every pair in the
// graph would be mostly noise for a swap interface that shows one chart.
var trackedPairs = [][2]string{
	{"WETH", "USDC"},
	{"WBTC", "USDC"},
	{"LINK", "USDC"},
	{"UNI", "USDC"},
	{"AAVE", "USDC"},
	{"DAI", "USDC"},
}

// currentPrices quotes one unit of each tracked base token against its quote
// token, using the best available route.
func (h *hub) currentPrices() []PriceTick {
	snapshot := h.holder.Get()
	if snapshot == nil {
		return nil
	}

	now := time.Now().Unix()
	out := make([]PriceTick, 0, len(trackedPairs))
	sc := amm.NewScratch()

	for _, pair := range trackedPairs {
		base, ok := dex.TokenBySymbol(pair[0])
		if !ok {
			continue
		}
		quote, ok := dex.TokenBySymbol(pair[1])
		if !ok {
			continue
		}

		price, ok := bestDirectPrice(snapshot, sc, base, quote)
		if !ok {
			continue
		}

		out = append(out, PriceTick{
			Pair:  base.Symbol + "/" + quote.Symbol,
			Price: price.String(),
			Time:  now,
		})
	}
	return out
}

// bestDirectPrice quotes one whole unit of base across every direct pool and
// returns the best result.
//
// One unit is small enough relative to pool depth that the answer is close to
// the marginal price, which is what a chart should show. Using the full
// routing search here would report a number that includes the price impact of
// whatever size happened to be chosen.
func bestDirectPrice(snapshot *graph.Snapshot, sc *amm.Scratch, base, quote dex.Token) (*big.Int, bool) {
	one := pow10(int(base.Decimals))

	var best *big.Int
	for _, e := range snapshot.EdgesFrom(base.Address) {
		if !strings.EqualFold(e.To, quote.Address) {
			continue
		}
		out, err := e.Pool.AmountOutInto(sc, one, base.Address)
		if err != nil || out.Sign() <= 0 {
			continue
		}
		if best == nil || out.Cmp(best) > 0 {
			best = new(big.Int).Set(out)
		}
	}
	return best, best != nil
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// The API is already fully public and read-only, and the browser app is
	// served from a different origin in development, so there is nothing for
	// an origin check to protect here.
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		return
	}

	c := &client{
		send: make(chan PriceTick, clientBuffer),
		done: make(chan struct{}),
	}
	s.hub.add(c)

	go s.readLoop(conn, c)
	s.writeLoop(conn, c)
}

// readLoop exists to notice the client going away and to keep the read
// deadline fresh. The stream is push-only, so anything received is discarded.
func (s *Server) readLoop(conn *websocket.Conn, c *client) {
	defer s.hub.remove(c)

	conn.SetReadLimit(512)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) writeLoop(conn *websocket.Conn, c *client) {
	ticker := time.NewTicker(pingPeriod)

	defer func() {
		ticker.Stop()
		s.hub.remove(c)
		_ = conn.Close()
	}()

	// Send the current prices immediately so a new chart is not blank until
	// the next broadcast.
	for _, t := range s.hub.currentPrices() {
		if err := writeTick(conn, t); err != nil {
			return
		}
	}

	for {
		select {
		case <-c.done:
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(writeWait))
			return

		case t := <-c.send:
			if err := writeTick(conn, t); err != nil {
				return
			}

		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func writeTick(conn *websocket.Conn, t PriceTick) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(t); err != nil {
		slog.Default().Debug("websocket write failed", "err", err)
		return err
	}
	return nil
}
