package main

func diskWorkloads() []Workload {
	return []Workload{
		{
			Name:           "seq-write",
			Suite:          "disk",
			Description:    "fio sequential write 256MB, bs=1M, direct=1, 15s steady-state",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command: `fio --name=seq-write --rw=write --bs=1m --size=256m \
				--direct=1 --ioengine=libaio --iodepth=32 --numjobs=1 \
				--runtime=15 --time_based \
				--filename=/tmp/fio_seq_write --output-format=json --group_reporting`,
			ParseResult: parseFioWriteBW,
		},
		{
			Name:           "seq-read",
			Suite:          "disk",
			Description:    "fio sequential read 256MB, bs=1M, direct=1, 15s steady-state",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command: `dd if=/dev/zero of=/tmp/fio_seq_read bs=1M count=256 conv=fdatasync 2>/dev/null && \
				fio --name=seq-read --rw=read --bs=1m --size=256m \
				--direct=1 --ioengine=libaio --iodepth=32 --numjobs=1 \
				--runtime=15 --time_based \
				--filename=/tmp/fio_seq_read --output-format=json --group_reporting`,
			ParseResult: parseFioReadBW,
		},
		{
			Name:           "rand-read-4k",
			Suite:          "disk",
			Description:    "fio random read 4K, 256MB file, iodepth=64, ramp=3s",
			Unit:           "IOPS",
			HigherIsBetter: true,
			Command: `dd if=/dev/zero of=/tmp/fio_rand_read bs=1M count=256 conv=fdatasync 2>/dev/null && \
				fio --name=rand-read --rw=randread --bs=4k --size=256m \
				--direct=1 --ioengine=libaio --iodepth=64 --numjobs=1 \
				--runtime=15 --time_based --ramp_time=3 \
				--filename=/tmp/fio_rand_read --output-format=json --group_reporting`,
			ParseResult: parseFioReadIOPS,
		},
		{
			Name:           "rand-write-4k",
			Suite:          "disk",
			Description:    "fio random write 4K, 256MB file, iodepth=64, ramp=3s",
			Unit:           "IOPS",
			HigherIsBetter: true,
			Command: `fio --name=rand-write --rw=randwrite --bs=4k --size=256m \
				--direct=1 --ioengine=libaio --iodepth=64 --numjobs=1 \
				--runtime=15 --time_based --ramp_time=3 \
				--filename=/tmp/fio_rand_write --output-format=json --group_reporting`,
			ParseResult: parseFioWriteIOPS,
		},
		{
			Name:           "fsync-latency",
			Suite:          "disk",
			Description:    "fio fsync latency: 4K write + fdatasync, 15s",
			Unit:           "us",
			HigherIsBetter: false,
			Command: `fio --name=fsync-lat --rw=write --bs=4k --size=256m \
				--ioengine=sync --fdatasync=1 --numjobs=1 \
				--runtime=15 --time_based \
				--filename=/tmp/fio_fsync --output-format=json --group_reporting`,
			ParseResult: parseFioSyncLatUs,
		},
		{
			Name:           "mixed-randrw",
			Suite:          "disk",
			Description:    "fio mixed random r/w 70:30, 4K, 256MB, iodepth=32, ramp=3s",
			Unit:           "IOPS",
			HigherIsBetter: true,
			Command: `fio --name=mixed-rw --rw=randrw --rwmixread=70 --bs=4k --size=256m \
				--direct=1 --ioengine=libaio --iodepth=32 --numjobs=1 \
				--runtime=15 --time_based --ramp_time=3 \
				--filename=/tmp/fio_mixed --output-format=json --group_reporting`,
			ParseResult: parseFioMixedIOPS,
		},
		{
			Name:           "small-file-create",
			Suite:          "disk",
			Description:    "Create+fsync+close 2000 small files (agent code-gen pattern)",
			Unit:           "files/s",
			HigherIsBetter: true,
			Command: `sh -c 'DIR=/tmp/bench_files && mkdir -p $DIR && \
				START=$(date +%s%N) && \
				for i in $(seq 1 2000); do \
					echo "import os; print(hello)" > $DIR/f_$i.py && \
					sync -d $DIR/f_$i.py 2>/dev/null; \
				done && \
				END=$(date +%s%N) && \
				rm -rf $DIR && \
				python3 -c "print(f\"{2000/((${END}-${START})/1e9):.0f}\")"'`,
			ParseResult: parseFloat,
		},
	}
}
