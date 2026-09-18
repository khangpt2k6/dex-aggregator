/**
 * Provenance strip for simulated pool state.
 *
 * Not dismissible and not styled as an error. Someone reading a five figure
 * output has to be able to see, without looking for it, whether the pools
 * behind it came from a node or from the deterministic simulator. One line is
 * enough to say so.
 */
export function SimulatedBanner() {
  return (
    <aside className="banner" aria-label="Data provenance">
      <span className="banner-tag">SIMULATED DATA</span>
      <span className="banner-text">
        Pool state from the built-in simulator, not mainnet. Set <code>ETH_RPC_URL</code> for live.
      </span>
    </aside>
  )
}
