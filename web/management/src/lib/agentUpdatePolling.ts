type Timer = ReturnType<typeof setTimeout>;

export function createAgentUpdatePolling<T>(options: {
  scope: () => string;
  load: (signal: AbortSignal) => Promise<T>;
  apply: (value: T) => void;
  error: (error: unknown) => void;
  loading: (loading: boolean) => void;
  canPoll: () => boolean;
  intervalMillis?: number;
}) {
  let stopped = false;
  let started = false;
  let revision = 0;
  let timer: Timer | undefined;
  let controller: AbortController | undefined;
  let pending: Promise<void> | undefined;
  let queued = false;

  function clearTimer() {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  }

  function schedule() {
    clearTimer();
    if (!started || stopped) return;
    timer = setTimeout(() => {
      timer = undefined;
      if (options.canPoll()) void refresh();
      else schedule();
    }, options.intervalMillis ?? 5000);
  }

  async function run() {
    do {
      queued = false;
      const requestRevision = revision;
      const scope = options.scope();
      const request = new AbortController();
      controller = request;
      options.loading(true);
      const current = () => !stopped && requestRevision === revision && scope === options.scope();
      try {
        const value = await options.load(request.signal);
        if (current()) options.apply(value);
      } catch (error) {
        if (current() && !request.signal.aborted) options.error(error);
      } finally {
        // A paired request can fail while its sibling is still running.
        request.abort();
        if (controller === request) controller = undefined;
      }
    } while (queued && !stopped);
    if (!stopped) options.loading(false);
  }

  function refresh(): Promise<void> {
    if (stopped) return Promise.resolve();
    clearTimer();
    // An explicit refresh after a mutation must perform a new read even when
    // an earlier read is still completing. Coalesce multiple callers into one.
    queued = true;
    if (!pending) {
      pending = run().finally(() => {
        pending = undefined;
        schedule();
      });
    }
    return pending;
  }

  return {
    refresh,
    start() { started = true; return refresh(); },
    scopeChanged() {
      revision += 1;
      controller?.abort();
      return refresh();
    },
    stop() {
      stopped = true;
      revision += 1;
      queued = false;
      clearTimer();
      controller?.abort();
      options.loading(false);
    },
  };
}
