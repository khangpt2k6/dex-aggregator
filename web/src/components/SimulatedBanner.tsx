/**
 * Provenance strip for simulated pool state.
 *
 * Not dismissible and not styled as an error. Someone reading a five figure
 * output has to be able to see, without looking for it, whether the pools
 * behind it came from a node or from the deterministic simulator. That is the
 * one thing this interface must never get wrong.
 */
export function SimulatedBanner() {
  return (
    <aside className="banner" aria-label="Data provenance">
      <span className="banner-tag">SIMULATED DATA</span>
      <p className="banner-text">
        Pool reserves, ticks and prices come from the deterministic simulator built into the
        aggregator, not from Ethereum mainnet. The routing, the maths and the latency are real; the
        liquidity is not. Set <code>ETH_RPC_URL</code> on the backend for live mainnet pools.
      </p>
    </aside>
  )
}
