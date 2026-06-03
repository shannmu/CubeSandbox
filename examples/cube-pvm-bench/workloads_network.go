package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func networkWorkloads() []Workload {
	return []Workload{
		{
			Name:        "loopback-throughput",
			Suite:       "network",
			Description: "Loopback TCP throughput over 5 seconds",
			Unit:        "Gbps",
			HigherIsBetter: true,
			Command: `python3 -c "
import socket,time,threading
SIZE=1024*1024
DURATION=5
data=b'x'*SIZE
total=[0]
done=[False]
def server():
    s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
    s.bind(('127.0.0.1',19999)); s.listen(1); c,_=s.accept()
    while not done[0]:
        d=c.recv(65536)
        if not d: break
        total[0]+=len(d)
    c.close(); s.close()
t=threading.Thread(target=server,daemon=True); t.start()
time.sleep(0.1)
c=socket.socket(); c.connect(('127.0.0.1',19999))
start=time.perf_counter()
while time.perf_counter()-start<DURATION:
    c.sendall(data)
c.close(); done[0]=True; t.join(timeout=2)
elapsed=time.perf_counter()-start
print(f'{total[0]*8/elapsed/1e9:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "gateway-ping",
			Suite:       "network",
			Description: "Ping RTT to default gateway (20 packets)",
			Unit:        "ms",
			HigherIsBetter: false,
			Command:     `ping -c 20 -i 0.1 $(ip route | grep default | awk '{print $3}') 2>/dev/null | tail -1`,
			ParseResult: parsePingOutput,
		},
	}
}

var pingRttRe = regexp.MustCompile(`([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)`)

func parsePingOutput(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	matches := pingRttRe.FindStringSubmatch(combined)
	if matches == nil {
		s := strings.TrimSpace(combined)
		v, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return v, nil
		}
		return 0, fmt.Errorf("cannot parse ping output: %q", s)
	}
	avg, err := strconv.ParseFloat(matches[2], 64)
	if err != nil {
		return 0, err
	}
	return avg, nil
}
