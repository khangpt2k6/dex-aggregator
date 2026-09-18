import { formatAge, formatMicros } from '../api/format'
import type { Status } from '../api/types'
import type { StreamStatus } from '../hooks/usePriceStream'

interface Props {
  status: Status | null
  error: string | null
  streamStatus: StreamStatus
  /** Router time of the most recent quote, in microseconds. */
  routerMicros: number | null
}

interface ReadoutProps {
  label: string
  value: string
  dim?: boolean
  dotClass?: string
}

function Readout({ label, value, dim = false, dotClass }: ReadoutProps) {
  return (
    <div className="readout">
      <span className="readout-label">{label}</span>
      <span className={dim ? 'readout-value is-dim' : 'readout-value'}>
        {dotClass !== undefined && <span className={dotClass} aria-hidden="true" />}
        {value}
      </span>
    </div>
  )
}

const STREAM_LABEL: Record<StreamStatus, string> = {
  connecting: 'OPENING',
  open: 'LIVE',
  retrying: 'RETRYING',
  closed: 'CLOSED',
}

/**
 * The instrument strip.
 *
 * Marked aria-live polite so a screen reader picks up a mode or pool count
 * change on its own schedule rather than interrupting whatever the person is
 * doing in the swap panel.
 */
export function StatusBar({ status, error, streamStatus, routerMicros }: Props) {
  const live = status?.mode === 'live'
  const indexer = status?.indexer

  return (
    <header className="masthead">
      <div className="brand">
        <svg
          className="brand-mark"
          width="22"
          height="22"
          viewBox="0 0 22 22"
          fill="none"
          aria-hidden="true"
        >
          <path d="M2 16h5l4-10 4 6h5" stroke="currentColor" strokeWidth="1.6" />
          <rect x="1" y="1" width="20" height="20" stroke="currentColor" strokeWidth="1" opacity="0.35" />
        </svg>
        <span>
          <h1 className="brand-name">ROUTR</h1>
          <p className="brand-sub">Route Terminal</p>
        </span>
      </div>

      <div className="readouts" aria-live="polite" aria-label="Aggregator status">
        <Readout
          label="Mode"
          value={status === null ? 'unknown' : live ? 'live mainnet' : 'simulated'}
          dotClass={status === null ? 'dot' : live ? 'dot is-live' : 'dot is-sim'}
        />
        <Readout label="Pools" value={indexer ? String(indexer.pools) : '--'} />
        <Readout label="Tokens" value={indexer ? String(indexer.tokens) : '--'} />
        <Readout label="Edges" value={indexer ? String(indexer.edges) : '--'} />
        <Readout
          label="Snapshot age"
          value={status ? formatAge(status.snapshotAgeMs) : '--'}
          dim
        />
        <Readout
          label="Router"
          value={routerMicros === null ? 'idle' : formatMicros(routerMicros)}
        />
        <Readout
          label="Stream"
          value={STREAM_LABEL[streamStatus]}
          dim
          dotClass={
            streamStatus === 'open' ? 'dot is-live' : streamStatus === 'retrying' ? 'dot is-sim' : 'dot'
          }
        />
        {error !== null && <Readout label="Poll" value={error} dotClass="dot is-down" dim />}
      </div>
    </header>
  )
}
