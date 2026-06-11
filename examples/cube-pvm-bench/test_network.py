import os
from e2b_code_interpreter import Sandbox

server_ip = os.environ.get("IPERF3_SERVER_IP", "")
server_port = os.environ.get("IPERF3_SERVER_PORT", "5201")

iperf3_test_code = f"""
import subprocess, sys
r = subprocess.run(
    ['iperf3', '-c', '{server_ip}', '-p', '{server_port}', '-t', '3', '-J'],
    capture_output=True, text=True, timeout=15
)
sys.stdout.write(r.stdout[-200:] if len(r.stdout) > 200 else r.stdout)
if r.returncode != 0:
    sys.stderr.write(r.stderr)
    raise RuntimeError(f"iperf3 exit {{r.returncode}}")
"""

connectivity_code = f"""
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(5)
try:
    s.connect(("{server_ip}", {server_port}))
    print(f"OK: connected to {server_ip}:{server_port}")
except Exception as e:
    print(f"FAIL: {{e}}")
finally:
    s.close()
"""

if not server_ip:
    print("ERROR: IPERF3_SERVER_IP not set")
    exit(1)

print(f"iperf3 server: {server_ip}:{server_port}")
print()

# ============ Test 1: WITHOUT allow_out ============
print("=" * 60)
print("Test 1: Sandbox WITHOUT allow_out (baseline)")
print("=" * 60)
try:
    with Sandbox.create(template=os.environ["CUBE_TEMPLATE_ID"]) as sb:
        print(f"  sandbox: {sb.sandbox_id}")
        r = sb.run_code(connectivity_code)
        print(f"  connectivity: {r}")
        if r.error:
            print(f"  error: {r}")
except Exception as e:
    print(f"  Exception: {e}")

print()

# ============ Test 2: WITH allow_out ============
print("=" * 60)
print(f"Test 2: Sandbox WITH network={{allow_out: ['{server_ip}/32']}}")
print("=" * 60)
try:
    with Sandbox.create(
        template=os.environ["CUBE_TEMPLATE_ID"],
        network={"allow_out": [f"{server_ip}/32"]},
    ) as sb:
        print(f"  sandbox: {sb.sandbox_id}")
        r = sb.run_code(connectivity_code)
        print(f"  connectivity: {r}")
        if r.error:
            print(f"  error: {r}")
        if "OK" in (r.text or ""):
            print("  --> Running iperf3 test...")
            r2 = sb.run_code(iperf3_test_code)
            print(f"  iperf3 result: {r2}")
            if r2.error:
                print(f"  iperf3 error: {r2}")
except Exception as e:
    print(f"  Exception: {e}")

print()

# ============ Test 3: WITH allow_out + allow_internet_access ============
print("=" * 60)
print(f"Test 3: Sandbox WITH allow_out + allow_internet_access=True")
print("=" * 60)
try:
    with Sandbox.create(
        template=os.environ["CUBE_TEMPLATE_ID"],
        allow_internet_access=True,
        network={"allow_out": [f"{server_ip}/32"]},
    ) as sb:
        print(f"  sandbox: {sb.sandbox_id}")
        r = sb.run_code(connectivity_code)
        print(f"  connectivity: {r}")
        if r.error:
            print(f"  error: {r}")
        if "OK" in (r.text or ""):
            print("  --> Running iperf3 test...")
            r2 = sb.run_code(iperf3_test_code)
            print(f"  iperf3 result: {r2}")
            if r2.error:
                print(f"  iperf3 error: {r2}")
except Exception as e:
    print(f"  Exception: {e}")

print()
print("Done.")
