import { useEffect, useRef, useState } from 'react'

import { fetchStatus } from '../api/client'
import { ApiError } from '../api/types'
import type { Status } from '../api/types'

export interface StatusState {
  status: Status | null
  /** Set when the last poll failed. The previous reading is kept alongside it. */
  error: string | null
  warming: boolean
}

/**
 * Polls /api/v1/status for the header readout.
 *
 * A failed poll never clears the last good reading. The header is an
 * instrument strip, and an instrument that blanks itself the moment it loses
 * a sample is worse than one that holds the last value and says so.
 */
export function useStatus(intervalMs = 4000): StatusState {
  const [state, setState] = useState<StatusState>({ status: null, error: null, warming: false })
  const alive = useRef(true)

  useEffect(() => {
    alive.current = true
    const controller = new AbortController()

    const poll = async () => {
      try {
        const status = await fetchStatus(controller.signal)
        if (!alive.current) return
        setState({ status, error: null, warming: false })
      } catch (cause) {
        if (!alive.current || controller.signal.aborted) return
        const warming = cause instanceof ApiError && cause.isWarmingUp
        const message = cause instanceof ApiError ? cause.detail || cause.message : 'status unavailable'
        setState((previous) => ({ status: previous.status, error: message, warming }))
      }
    }

    void poll()
    const timer = window.setInterval(() => void poll(), intervalMs)

    return () => {
      alive.current = false
      controller.abort()
      window.clearInterval(timer)
    }
  }, [intervalMs])

  return state
}
