# Local process benchmark

Started: 2026-10-18T15:41:13Z. Finished: 2026-10-18T15:43:57Z.

Environment: darwin 27.0.1 / arm64; Apple M5 Pro; 18 logical CPUs; memory 25769803776; harness go1.27.2.

Source: `34d5a97f4a40c2bf88a7123dbb1273d71d64fc27`; worktree dirty: true; product source dirty: true. sertun build: `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=<commit>'`.

Workload: 4 trials/configuration; 4s per bulk phase; 100 short requests/trial (64-byte echo); 8 parallel streams; 10 warmup requests; 1s settle + 1s idle observation. All endpoints use loopback.

Optimization baseline: `34d5a97f4a40c2bf88a7123dbb1273d71d64fc27`, rebuilt with the same compiler and flags as the working-tree sertun. All variants are interleaved in the same run.

| Tool | Version | Build Go | Binary MiB | SHA-256 |
| --- | --- | --- | ---: | --- |
| sertun | 34d5a97f4a40c2bf88a7123dbb1273d71d64fc27 | go1.27.2 | 4.88 | `fe38fac7f027316bb185da5f8c7060962ad09b7b1d2a10b5202f359bbf744683` |
| frpc | v0.71.1 | go1.26.8 | 14.45 | `7aaaba058dbeeedc86be876c44912c4f33756ab3e6e7a085f8d7af7814a073c1` |
| frps | v0.71.1 | go1.26.8 | 17.93 | `96165f1f8bb462d234d1c374e35176d5cf05c24dd16f710b75e33af45376dc74` |
| mihomo | v1.19.32 | go1.26.8 | 54.15 | `94a386ec0149080deadd86b1f667363bde3c70f7489dba3258e52c56fc9a6d66` |
| sertun-baseline | 34d5a97f4a40c2bf88a7123dbb1273d71d64fc27 | go1.27.2 | 4.88 | `973639cf7faeac2e9a5a4b510228f6ee3fc7c8a62759fd3aa05103a2bcc327ab` |

Values: median [minimum, maximum] across trials. Throughput counts received echo payload once; the payload also traverses the opposite direction. RSS sums client + server and is sampled at 200ms intervals. CPU 100% = one logical core; ps CPU time resolution is platform-dependent (macOS 0.01s, Linux typically 1s).

| Scenario | Variant | Tunnel idle MiB | Outer idle MiB | Short p50 ms | Short p95 ms | Single MiB/s | Parallel MiB/s |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| direct | sertun | 23.35 [23.19, 23.62] | 0.00 [0.00, 0.00] | 0.36 [0.33, 0.42] | 0.89 [0.64, 0.95] | 553.72 [455.09, 596.99] | 614.54 [497.70, 643.96] |
| direct | sertun-baseline | 24.46 [23.83, 24.52] | 0.00 [0.00, 0.00] | 0.36 [0.33, 0.47] | 0.89 [0.64, 0.98] | 444.14 [422.34, 447.83] | 528.54 [453.95, 605.79] |
| direct | frp-plain | 45.80 [45.41, 46.42] | 0.00 [0.00, 0.00] | 0.37 [0.34, 0.39] | 0.70 [0.51, 0.75] | 461.85 [436.06, 492.84] | 549.43 [426.60, 583.13] |
| direct | frp-mux | 46.30 [45.95, 46.67] | 0.00 [0.00, 0.00] | 0.40 [0.38, 0.43] | 0.83 [0.64, 0.85] | 662.92 [653.26, 668.92] | 482.94 [478.06, 487.45] |

| Scenario | Variant | Single tunnel CPU % | Single outer CPU % | Parallel tunnel CPU % | Parallel outer CPU % |
| --- | --- | ---: | ---: | ---: | ---: |
| direct | sertun | 201.12 [189.25, 207.24] | 0.00 [0.00, 0.00] | 143.12 [126.75, 158.00] | 0.00 [0.00, 0.00] |
| direct | sertun-baseline | 213.12 [211.25, 213.75] | 0.00 [0.00, 0.00] | 153.62 [152.50, 164.00] | 0.00 [0.00, 0.00] |
| direct | frp-plain | 210.50 [203.50, 215.75] | 0.00 [0.00, 0.00] | 153.25 [150.00, 166.00] | 0.00 [0.00, 0.00] |
| direct | frp-mux | 310.99 [305.75, 314.24] | 0.00 [0.00, 0.00] | 343.74 [337.24, 345.75] | 0.00 [0.00, 0.00] |

CPU cost counts summed process CPU seconds per GiB of received echo payload; lower is better. Tunnel + outer excludes the harness and backend. Each trial is normalized before median/range aggregation.

| Scenario | Variant | Single tunnel CPU s/GiB | Single tunnel + outer CPU s/GiB | Parallel tunnel CPU s/GiB | Parallel tunnel + outer CPU s/GiB |
| --- | --- | ---: | ---: | ---: | ---: |
| direct | sertun | 3.74 [3.25, 4.62] | 3.74 [3.25, 4.62] | 2.44 [2.02, 3.10] | 2.44 [2.02, 3.10] |
| direct | sertun-baseline | 4.90 [4.86, 5.18] | 4.90 [4.86, 5.18] | 3.08 [2.59, 3.47] | 3.08 [2.59, 3.47] |
| direct | frp-plain | 4.69 [4.23, 5.03] | 4.69 [4.23, 5.03] | 2.98 [2.63, 3.70] | 2.98 [2.63, 3.70] |
| direct | frp-mux | 4.80 [4.75, 4.86] | 4.80 [4.75, 4.86] | 7.27 [7.19, 7.34] | 7.27 [7.19, 7.34] |

frp-plain: TCP mux/TLS/payload encryption/compression disabled, poolCount=0. frp-mux: same settings with TCP mux enabled. AnyTLS/HY2 use real local Mihomo endpoints and an ephemeral self-signed certificate; only certificate trust verification is skipped. No WAN loss, RTT, reconnect or outer half-close claims follow from these workloads. Official releases may use different Go compilers, as recorded above.
