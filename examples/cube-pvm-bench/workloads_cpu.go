package main

import "fmt"

func cpuWorkloads() []Workload {
	return []Workload{
		// --- Single-core: arithmetic latency ---
		{
			Name:           "cpu-int-add",
			Suite:          "cpu",
			Description:    "lmbench integer add latency (ns per op)",
			Unit:           "ns",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_ops 2>&1 | grep 'integer add' | awk '{print $NF}'`, lmbenchBinDir),
			ParseResult:    parseFloat,
		},
		{
			Name:           "cpu-int-div",
			Suite:          "cpu",
			Description:    "lmbench integer div latency (ns per op)",
			Unit:           "ns",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_ops 2>&1 | grep 'integer div' | awk '{print $NF}'`, lmbenchBinDir),
			ParseResult:    parseFloat,
		},
		{
			Name:           "cpu-double-add",
			Suite:          "cpu",
			Description:    "lmbench double-precision add latency (ns per op)",
			Unit:           "ns",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_ops 2>&1 | grep 'double add' | awk '{print $NF}'`, lmbenchBinDir),
			ParseResult:    parseFloat,
		},
		// --- Multi-core: openssl parallel ---
		{
			Name:           "cpu-aes-1t",
			Suite:          "cpu",
			Description:    "openssl AES-256-CBC single-thread throughput (16KB blocks, 10s)",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        `openssl speed -elapsed -seconds 10 -bytes 16384 aes-256-cbc 2>&1 | tail -1 | awk '{print $NF/1024/1024}'`,
			ParseResult:    parseFloat,
		},
		{
			Name:           "cpu-aes-mt",
			Suite:          "cpu",
			Description:    "openssl AES-256-CBC multi-thread throughput (16KB blocks, 10s, all cores)",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        `openssl speed -elapsed -seconds 10 -bytes 16384 -multi $(nproc) aes-256-cbc 2>&1 | tail -1 | awk '{print $NF/1024/1024}'`,
			ParseResult:    parseFloat,
		},
		{
			Name:           "cpu-sha256-1t",
			Suite:          "cpu",
			Description:    "openssl SHA256 single-thread throughput (16KB blocks, 10s)",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        `openssl speed -elapsed -seconds 10 -bytes 16384 sha256 2>&1 | tail -1 | awk '{print $NF/1024/1024}'`,
			ParseResult:    parseFloat,
		},
		{
			Name:           "cpu-sha256-mt",
			Suite:          "cpu",
			Description:    "openssl SHA256 multi-thread throughput (16KB blocks, 10s, all cores)",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        `openssl speed -elapsed -seconds 10 -bytes 16384 -multi $(nproc) sha256 2>&1 | tail -1 | awk '{print $NF/1024/1024}'`,
			ParseResult:    parseFloat,
		},
		// --- Inter-core communication: IPC ---
		{
			Name:           "cpu-ipc-unix-lat",
			Suite:          "cpu",
			Description:    "lmbench unix socket latency: inter-process round-trip (200 iterations)",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_unix -N 200 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "cpu-ipc-unix-bw",
			Suite:          "cpu",
			Description:    "lmbench unix socket bandwidth: inter-process throughput",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`%s/bw_unix 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchBW,
		},
		// --- CPU + memory mixed ---
		{
			Name:           "cpu-compress-gzip",
			Suite:          "cpu",
			Description:    "gzip compression throughput: 128MB random data (CPU+memory intensive)",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        `dd if=/dev/urandom bs=1M count=128 2>/dev/null | { time -p gzip > /dev/null; } 2>&1 | awk '/^real/{printf "%.2f", 128/$2}'`,
			ParseResult:    parseFloat,
		},
	}
}
