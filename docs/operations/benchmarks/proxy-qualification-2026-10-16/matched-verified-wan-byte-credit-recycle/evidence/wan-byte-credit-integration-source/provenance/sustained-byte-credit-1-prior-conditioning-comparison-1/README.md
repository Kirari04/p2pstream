# Prior sustained conditioning comparison

This read-only comparison answers why `sustained-byte-credit-1` lost reuse conditioning while the prior `sustained-below-tls-2` cohort retained it. The prior revisit idle ages were 24.751–26.724s. The current failed revisits were already 30.834–31.171s old at the next request start and 30.916–31.252s old at GotConn, so they crossed the fixed 30s limit before new-connection establishment.

For the four changed lanes, the three intervening measured transfer walls increased by 4.274–4.978s. The remainder changed by less than 0.2s. That directly explains the conditioning difference at the request-timeline level. It does not identify why those intervening transfers were slower and supports neither a timeout change nor a retry.
