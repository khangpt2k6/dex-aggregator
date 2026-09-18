import { useEffect, useMemo, useRef, useState } from 'react'
import { ColorType, CrosshairMode, LineSeries, LineStyle, createChart } from 'lightweight-charts'
import type { IChartApi, ISeriesApi, LineData, UTCTimestamp } from 'lightweight-charts'

import type { PricePoint, StreamStatus } from '../hooks/usePriceStream'

interface Props {
  pairs: string[]
  selected: string
  onSelect: (pair: string) => void
  points: PricePoint[]
  status: StreamStatus
}

/** Visible window per timeframe, in seconds. LIVE fits the whole session. */
const TIMEFRAMES = [
  { id: '1M', seconds: 60 },
  { id: '5M', seconds: 300 },
  { id: '15M', seconds: 900 },
  { id: 'LIVE', seconds: 0 },
] as const

type TimeframeId = (typeof TIMEFRAMES)[number]['id']

/** Decimal places that suit the magnitude being plotted. */
function precisionFor(value: number): number {
  if (value >= 1000) return 2
  if (value >= 1) return 4
  return 6
}

function toLine(points: PricePoint[]): LineData<UTCTimestamp>[] {
  return points.map((point) => ({ time: point.time as UTCTimestamp, value: point.value }))
}

/**
 * Price line for the selected pair.
 *
 * Two series share one scale: a dim one for the locally seeded warm-up and the
 * accent one for ticks actually received from the stream. Drawing them the same
 * way would quietly claim the seeded section was observed, so the header
 * carries a two-swatch legend instead of a caption.
 */
export function PriceChart({ pairs, selected, onSelect, points, status }: Props) {
  const host = useRef<HTMLDivElement | null>(null)
  const chart = useRef<IChartApi | null>(null)
  const seedSeries = useRef<ISeriesApi<'Line'> | null>(null)
  const liveSeries = useRef<ISeriesApi<'Line'> | null>(null)
  const fitted = useRef<string>('')

  const [timeframe, setTimeframe] = useState<TimeframeId>('LIVE')

  const [base, quote] = useMemo(() => {
    const parts = selected.split('/')
    return [parts[0] ?? '', parts[1] ?? '']
  }, [selected])

  const last = points.length > 0 ? points[points.length - 1] : undefined
  const firstLive = points.find((point) => point.live)

  const delta =
    last !== undefined && firstLive !== undefined && firstLive.value > 0 && last !== firstLive
      ? ((last.value - firstLive.value) / firstLive.value) * 100
      : null

  useEffect(() => {
    const element = host.current
    if (element === null) return

    const instance = createChart(element, {
      autoSize: true,
      layout: {
        background: { type: ColorType.Solid, color: '#121418' },
        textColor: '#77828f',
        fontFamily: "'JetBrains Mono', ui-monospace, monospace",
        fontSize: 11,
        attributionLogo: false,
      },
      grid: {
        vertLines: { color: '#1a1e24', style: LineStyle.Solid },
        horzLines: { color: '#1a1e24', style: LineStyle.Solid },
      },
      rightPriceScale: {
        borderColor: '#232830',
        scaleMargins: { top: 0.14, bottom: 0.1 },
      },
      timeScale: {
        borderColor: '#232830',
        timeVisible: true,
        secondsVisible: true,
        rightOffset: 3,
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: '#2f3641', width: 1, style: LineStyle.Dotted, labelBackgroundColor: '#1d8ea3' },
        horzLine: { color: '#2f3641', width: 1, style: LineStyle.Dotted, labelBackgroundColor: '#1d8ea3' },
      },
      handleScale: { axisPressedMouseMove: false },
    })

    seedSeries.current = instance.addSeries(LineSeries, {
      color: '#3a424e',
      lineWidth: 1,
      lineStyle: LineStyle.Solid,
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: false,
    })

    liveSeries.current = instance.addSeries(LineSeries, {
      color: '#35d0e8',
      lineWidth: 2,
      priceLineVisible: true,
      priceLineColor: '#1d8ea3',
      priceLineStyle: LineStyle.Dotted,
      lastValueVisible: true,
      crosshairMarkerRadius: 3,
      crosshairMarkerBorderColor: '#35d0e8',
      crosshairMarkerBackgroundColor: '#0a0b0d',
    })

    chart.current = instance

    return () => {
      instance.remove()
      chart.current = null
      seedSeries.current = null
      liveSeries.current = null
    }
  }, [])

  useEffect(() => {
    const seed = seedSeries.current
    const live = liveSeries.current
    const instance = chart.current
    if (seed === null || live === null || instance === null) return

    const seedPoints = points.filter((point) => !point.live)
    const livePoints = points.filter((point) => point.live)

    // Repeat the first live point on the seeded series so the two lines meet
    // instead of leaving a gap at the seam.
    const bridged = livePoints.length > 0 ? [...seedPoints, livePoints[0] as PricePoint] : seedPoints

    const reference = livePoints[livePoints.length - 1]?.value ?? seedPoints[0]?.value ?? 1
    const precision = precisionFor(reference)
    const priceFormat = { type: 'price', precision, minMove: 1 / 10 ** precision } as const

    seed.applyOptions({ priceFormat })
    live.applyOptions({ priceFormat })

    seed.setData(toLine(bridged))
    live.setData(toLine(livePoints))

    if (points.length === 0) return

    const window = TIMEFRAMES.find((frame) => frame.id === timeframe)?.seconds ?? 0
    const edge = points[points.length - 1]?.time

    if (window > 0 && edge !== undefined) {
      instance.timeScale().setVisibleRange({
        from: (edge - window) as UTCTimestamp,
        to: edge as UTCTimestamp,
      })
      return
    }

    if (fitted.current !== selected) {
      fitted.current = selected
      instance.timeScale().fitContent()
    }
  }, [points, selected, timeframe])

  const priceText = last === undefined ? '--' : last.value.toFixed(precisionFor(last.value))

  return (
    <section className="panel area-chart" aria-label="Price chart">
      <div className="panel-head">
        <h2 className="label label-lead">Price</h2>

        <div className="token-select">
          <select aria-label="Chart pair" value={selected} onChange={(event) => onSelect(event.target.value)}>
            {(pairs.includes(selected) ? pairs : [selected, ...pairs]).map((pair) => (
              <option key={pair} value={pair}>
                {pair}
              </option>
            ))}
          </select>
        </div>

        <div className="hops-buttons" role="group" aria-label="Timeframe">
          {TIMEFRAMES.map((frame) => (
            <button
              key={frame.id}
              type="button"
              aria-pressed={frame.id === timeframe}
              onClick={() => {
                setTimeframe(frame.id)
                if (frame.seconds === 0) fitted.current = ''
              }}
            >
              {frame.id}
            </button>
          ))}
        </div>

        <span className="spacer" />

        <div className="legend" aria-hidden="true">
          <span className="legend-item">
            <span className="legend-swatch is-seeded" />
            Seeded
          </span>
          <span className="legend-item">
            <span className="legend-swatch is-live" />
            Live
          </span>
        </div>

        <span className="chart-price mono">
          {priceText}
          <small>
            {quote} / {base || 'token'}
          </small>
        </span>

        {delta !== null && (
          <span className={`impact-pill ${delta >= 0 ? 'is-good' : 'is-severe'}`}>
            <span className="impact-key">Session</span>
            {delta >= 0 ? '+' : ''}
            {delta.toFixed(2)}%
          </span>
        )}
      </div>

      <div className="chart-canvas">
        <div ref={host} style={{ position: 'absolute', inset: 0 }} />
        {points.length === 0 && (
          <p className={status === 'open' ? 'chart-empty pulse' : 'chart-empty'}>
            {status === 'open' ? 'WAITING FOR FIRST TICK' : 'PRICE STREAM DISCONNECTED'}
          </p>
        )}
      </div>
    </section>
  )
}
