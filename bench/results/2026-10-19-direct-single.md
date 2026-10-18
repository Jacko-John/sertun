# Local process benchmark

Started: 2026-10-18T17:52:44Z. Finished: 2026-10-18T17:54:53Z.

Environment: darwin 27.0.1 / arm64; Apple M5 Pro; 18 logical CPUs; memory 25769803776; harness go1.27.2.

Source: `34d5a97f4a40c2bf88a7123dbb1273d71d64fc27`; worktree dirty: true; product source dirty: true. sertun build: `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=<commit>'`.

Workload: 3 trials/configuration; 6s per bulk phase; 100 short requests/trial (64-byte echo); 8 parallel streams; 10 warmup requests; 1s settle + 1s idle observation. All endpoints use loopback.

| Tool | Version | Build Go | Binary MiB | SHA-256 |
| --- | --- | --- | ---: | --- |
| sertun | 34d5a97f4a40c2bf88a7123dbb1273d71d64fc27 | go1.27.2 | 4.89 | `6f3c6437a2ba474b3cf7a04850d5cf4a17e7494c2266eaf39090988272582506` |
| frpc | v0.71.1 | go1.26.8 | 14.45 | `7aaaba058dbeeedc86be876c44912c4f33756ab3e6e7a085f8d7af7814a073c1` |
| frps | v0.71.1 | go1.26.8 | 17.93 | `96165f1f8bb462d234d1c374e35176d5cf05c24dd16f710b75e33af45376dc74` |
| mihomo | v1.19.32 | go1.26.8 | 54.15 | `94a386ec0149080deadd86b1f667363bde3c70f7489dba3258e52c56fc9a6d66` |

Values: median [minimum, maximum] across trials. Throughput counts received echo payload once; the payload also traverses the opposite direction. RSS sums client + server and is sampled at 200ms intervals. CPU 100% = one logical core; ps CPU time resolution is platform-dependent (macOS 0.01s, Linux typically 1s).

| Scenario | Variant | Tunnel idle MiB | Outer idle MiB | Short p50 ms | Short p95 ms | Single MiB/s | Parallel MiB/s |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| direct | sertun | 23.45 [22.94, 23.62] | 0.00 [0.00, 0.00] | 0.40 [0.34, 0.42] | 0.80 [0.64, 0.81] | 505.92 [482.63, 539.85] | 499.26 [499.24, 679.63] |
| direct | frp-plain | 45.92 [45.84, 46.58] | 0.00 [0.00, 0.00] | 0.43 [0.43, 0.44] | 0.65 [0.62, 0.69] | 411.41 [410.44, 439.35] | 504.36 [480.96, 523.42] |
| direct | frp-mux | 46.39 [46.12, 47.02] | 0.00 [0.00, 0.00] | 0.39 [0.39, 0.43] | 0.65 [0.57, 0.83] | 626.53 [609.50, 644.08] | 438.66 [434.55, 450.19] |

| Scenario | Variant | Single tunnel CPU % | Single outer CPU % | Parallel tunnel CPU % | Parallel outer CPU % |
| --- | --- | ---: | ---: | ---: | ---: |
| direct | sertun | 203.50 [197.67, 213.83] | 0.00 [0.00, 0.00] | 134.99 [122.33, 137.33] | 0.00 [0.00, 0.00] |
| direct | frp-plain | 212.16 [209.16, 212.66] | 0.00 [0.00, 0.00] | 146.33 [142.83, 154.00] | 0.00 [0.00, 0.00] |
| direct | frp-mux | 309.49 [300.99, 309.66] | 0.00 [0.00, 0.00] | 338.66 [336.50, 338.99] | 0.00 [0.00, 0.00] |

CPU cost counts summed process CPU seconds per GiB of received echo payload; lower is better. Tunnel + outer excludes the harness and backend. Each trial is normalized before median/range aggregation.

| Scenario | Variant | Single tunnel CPU s/GiB | Single tunnel + outer CPU s/GiB | Parallel tunnel CPU s/GiB | Parallel tunnel + outer CPU s/GiB |
| --- | --- | ---: | ---: | ---: | ---: |
| direct | sertun | 4.32 [3.75, 4.33] | 4.32 [3.75, 4.33] | 2.77 [1.84, 2.82] | 2.77 [1.84, 2.82] |
| direct | frp-plain | 5.29 [4.87, 5.29] | 5.29 [4.87, 5.29] | 3.01 [2.90, 3.12] | 3.01 [2.90, 3.12] |
| direct | frp-mux | 5.06 [4.92, 5.06] | 5.06 [4.92, 5.06] | 7.91 [7.65, 7.99] | 7.91 [7.65, 7.99] |

frp-plain: TCP mux/TLS/payload encryption/compression disabled. The configuration specified poolCount=0, which frp v0.71.1 defaults to an effective poolCount=1. frp-mux: same settings with TCP mux enabled. AnyTLS/HY2 use real local Mihomo endpoints and an ephemeral self-signed certificate; only certificate trust verification is skipped. No WAN loss, RTT, reconnect or outer half-close claims follow from these workloads. Official releases may use different Go compilers, as recorded above.
