#!/usr/bin/env bash
# Sistemo v0.5.0 Integration Test Suite
# Run as: sudo bash scripts/test-v050.sh
# Requires: sistemo binary already built (./sistemo), daemon NOT running
#
# Build first (as your user, NOT root):
#   go build -o sistemo ./cmd/sistemo && go test -count=1 ./...
# Then run:
#   sudo bash scripts/test-v050.sh

set -uo pipefail

SISTEMO="./sistemo"
# When run via sudo, HOME is /root but sistemo data is in the original user's home
REAL_HOME="${SUDO_USER:+$(eval echo ~$SUDO_USER)}"
REAL_HOME="${REAL_HOME:-$HOME}"
DB="$REAL_HOME/.sistemo/sistemo.db"
export HOME="$REAL_HOME"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
PASS=0
FAIL=0

pass() { echo -e "  ${GREEN}PASS${NC} $1"; ((PASS++)); }
fail() { echo -e "  ${RED}FAIL${NC} $1"; ((FAIL++)); }
skip() { echo -e "  ${YELLOW}SKIP${NC} $1"; }
section() { echo -e "\n${YELLOW}=== $1 ===${NC}"; }

assert_contains() {
    if echo "$1" | grep -q "$2"; then pass "$3"; else fail "$3 (expected '$2')"; fi
}
assert_not_contains() {
    if echo "$1" | grep -q "$2"; then fail "$3 (unexpected '$2')"; else pass "$3"; fi
}
assert_exit_fail() {
    if eval "$1" >/dev/null 2>&1; then fail "$2 (expected failure)"; else pass "$2"; fi
}

cleanup_all() {
    $SISTEMO vm delete test1 2>/dev/null || true
    $SISTEMO vm delete test2 2>/dev/null || true
    $SISTEMO vm delete test3 2>/dev/null || true
    $SISTEMO vm delete test4 2>/dev/null || true
    $SISTEMO vm delete test5 2>/dev/null || true
    $SISTEMO vm delete porttest 2>/dev/null || true
    $SISTEMO vm delete killtest 2>/dev/null || true
    $SISTEMO vm delete failvm 2>/dev/null || true
    $SISTEMO volume delete mydata 2>/dev/null || true
    $SISTEMO volume delete extra 2>/dev/null || true
    $SISTEMO volume delete vol1 2>/dev/null || true
    $SISTEMO volume delete vol2 2>/dev/null || true
    $SISTEMO network delete testnet 2>/dev/null || true
}

# ─── Pre-flight ───────────────────────────────────────────
section "0. Pre-flight"

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root (sudo). Exiting."
    exit 1
fi

if [ ! -f "$SISTEMO" ]; then
    echo "Binary not found. Build first: go build -o sistemo ./cmd/sistemo"
    exit 1
fi
pass "Binary exists"

# Kill any existing daemon
pkill -f "sistemo up" 2>/dev/null || true
sleep 1

# Start daemon
echo "Starting daemon..."
$SISTEMO up > /tmp/sistemo-daemon.log 2>&1 &
DAEMON_PID=$!
sleep 3

OUT=$($SISTEMO doctor 2>&1) || true
assert_contains "$OUT" "checks passed" "Doctor passes"

# Clean any leftovers
cleanup_all

# ─── Test 1: Deploy creates root volume ───────────────────
section "1. Deploy creates root volume"

$SISTEMO vm deploy debian --name test1 --memory 1GB >/dev/null 2>&1
pass "Deploy test1 succeeded"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "test1-root" "Root volume test1-root created"
assert_contains "$OUT" "attached" "Root volume is attached"

VMID=$(sqlite3 $DB "SELECT id FROM vm WHERE name='test1'")
VMDIR="$HOME/.sistemo/vms/$VMID"
if [ ! -f "$VMDIR/rootfs.ext4" ]; then pass "No rootfs.ext4 in vmDir"; else fail "rootfs.ext4 should not be in vmDir"; fi

OUT=$($SISTEMO vm status test1 2>&1)
assert_contains "$OUT" "Volumes:" "VM status shows volumes section"
assert_contains "$OUT" "test1-root" "VM status shows root volume name"

OUT=$($SISTEMO vm list 2>&1)
assert_contains "$OUT" "VOLUMES" "VM list has VOLUMES column"

# ─── Test 2: Deploy with --storage size ───────────────────
section "2. Deploy with --storage size"

$SISTEMO vm deploy debian --name test2 --memory 512 --storage 5GB >/dev/null 2>&1
pass "Deploy test2 with 5GB storage succeeded"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "5120" "test2-root shows 5120 MB"

# ─── Test 3: Deploy with extra volume ─────────────────────
section "3. Deploy with extra volume (--attach)"

$SISTEMO volume create 512 --name mydata >/dev/null 2>&1
pass "Created mydata volume"

$SISTEMO vm deploy debian --name test3 --memory 512 --attach mydata >/dev/null 2>&1
pass "Deploy test3 with --attach mydata succeeded"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "test3-root" "test3-root volume exists"
assert_contains "$OUT" "mydata" "mydata volume exists"

# ─── Test 4: Stop / start preserves volumes ───────────────
section "4. Stop / start preserves volumes"

$SISTEMO vm stop test1 >/dev/null 2>&1
pass "Stopped test1"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "test1-root" "Root volume still exists after stop"
assert_contains "$OUT" "attached" "Root volume still attached after stop"

$SISTEMO vm start test1 >/dev/null 2>&1
pass "Started test1 from existing root volume"

# ─── Test 5: Volume resize ───────────────────────────────
section "5. Volume resize"

$SISTEMO vm stop test1 >/dev/null 2>&1
$SISTEMO volume resize test1-root 4096 >/dev/null 2>&1
pass "Resized test1-root to 4096 MB"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "4096" "Volume list shows 4096 MB"

$SISTEMO vm start test1 >/dev/null 2>&1
pass "Started test1 with resized volume"

# ─── Test 6: Resize guards ───────────────────────────────
section "6. Volume resize guards"

assert_exit_fail "$SISTEMO volume resize test1-root 8192 2>&1" "Cannot resize while VM is running"

$SISTEMO vm stop test1 >/dev/null 2>&1
assert_exit_fail "$SISTEMO volume resize test1-root 1024 2>&1" "Cannot shrink volume"

$SISTEMO vm start test1 >/dev/null 2>&1

# ─── Test 7: Attach / detach ─────────────────────────────
section "7. Attach / detach"

$SISTEMO volume create 256 --name extra >/dev/null 2>&1
pass "Created extra volume"

assert_exit_fail "$SISTEMO volume attach test1 extra 2>&1" "Cannot attach to running VM"

$SISTEMO vm stop test1 >/dev/null 2>&1
$SISTEMO volume attach test1 extra >/dev/null 2>&1
pass "Attached extra to stopped test1"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "extra" "Extra volume in list"

$SISTEMO vm start test1 >/dev/null 2>&1
pass "Started test1 with extra volume"

assert_exit_fail "$SISTEMO volume detach test1 extra 2>&1" "Cannot detach from running VM"

$SISTEMO vm stop test1 >/dev/null 2>&1
$SISTEMO volume detach test1 extra >/dev/null 2>&1
pass "Detached extra from stopped test1"

OUT=$($SISTEMO volume list 2>&1)
# extra should show online
assert_contains "$OUT" "online" "Extra volume is online after detach"

$SISTEMO vm start test1 >/dev/null 2>&1

# ─── Test 8: VM delete releases volumes ──────────────────
section "8. VM delete releases volumes"

$SISTEMO vm delete test3 >/dev/null 2>&1
pass "Deleted test3"

OUT=$($SISTEMO volume list 2>&1)
assert_not_contains "$OUT" "test3-root" "test3-root deleted with VM"
assert_contains "$OUT" "mydata" "mydata volume preserved after VM delete"

# Check mydata is online
assert_contains "$OUT" "online" "mydata is online (detached)"

# ─── Test 9: VM delete with --preserve-storage ───────────
section "9. VM delete with --preserve-storage"

$SISTEMO vm deploy debian --name test4 --memory 512 >/dev/null 2>&1
pass "Deployed test4"

$SISTEMO vm delete test4 --preserve-storage >/dev/null 2>&1
pass "Deleted test4 with --preserve-storage"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "test4-root" "test4-root preserved after delete"
assert_contains "$OUT" "online" "test4-root is online (detached)"

# Clean up preserved volume
$SISTEMO volume delete test4-root >/dev/null 2>&1

# ─── Test 10: Failed deploy cleanup ──────────────────────
section "10. Failed deploy cleanup"

dd if=/dev/zero of=/tmp/bad.ext4 bs=1M count=1 2>/dev/null
$SISTEMO vm deploy /tmp/bad.ext4 --name failvm 2>/dev/null || true

OUT=$($SISTEMO vm list 2>&1)
assert_not_contains "$OUT" "failvm" "failvm not in vm list"

OUT=$($SISTEMO volume list 2>&1)
assert_not_contains "$OUT" "failvm" "failvm-root not in volume list"

rm -f /tmp/bad.ext4

# ─── Test 11: Volume CRUD ────────────────────────────────
section "11. Volume CRUD standalone"

$SISTEMO volume create 100 --name vol1 >/dev/null 2>&1
$SISTEMO volume create 200 --name vol2 >/dev/null 2>&1
pass "Created vol1 and vol2"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "vol1" "vol1 in list"
assert_contains "$OUT" "vol2" "vol2 in list"

$SISTEMO volume delete vol2 >/dev/null 2>&1
pass "Deleted vol2"

OUT=$($SISTEMO volume list 2>&1)
assert_not_contains "$OUT" "vol2" "vol2 gone from list"

$SISTEMO vm deploy debian --name test5 --memory 512 --attach vol1 >/dev/null 2>&1
assert_exit_fail "$SISTEMO volume delete vol1 2>&1" "Cannot delete attached volume"

$SISTEMO vm delete test5 >/dev/null 2>&1
$SISTEMO volume delete vol1 >/dev/null 2>&1
pass "Deleted vol1 after VM delete"

# ─── Test 12: Port forwarding ────────────────────────────
section "12. Port forwarding (regression)"

$SISTEMO vm deploy debian --name porttest --memory 512 --expose 4040:80 >/dev/null 2>&1
sleep 1

RULES=$(iptables -t nat -L OUTPUT -n 2>/dev/null | grep 4040 || true)
assert_contains "$RULES" "4040" "DNAT rule for port 4040 exists"

$SISTEMO vm delete porttest >/dev/null 2>&1
RULES=$(iptables -t nat -L OUTPUT -n 2>/dev/null | grep 4040 || true)
assert_not_contains "$RULES" "4040" "DNAT rule cleaned after delete"

# ─── Test 13: Named network ──────────────────────────────
section "13. Named network"

$SISTEMO network create testnet >/dev/null 2>&1
pass "Created testnet"

$SISTEMO vm deploy debian --name test1 --memory 512 --network testnet --expose 5050:80 >/dev/null 2>&1
pass "Deployed on testnet with port 5050"

LOCALNET=$(sysctl -n net.ipv4.conf.br-testnet.route_localnet 2>/dev/null || echo "0")
if [ "$LOCALNET" = "1" ]; then pass "route_localnet=1 on br-testnet"; else fail "route_localnet not set"; fi

$SISTEMO vm delete test1 >/dev/null 2>&1
$SISTEMO network delete testnet >/dev/null 2>&1

# ─── Test 14: Reconciler ─────────────────────────────────
section "14. Reconciler (kill process)"

$SISTEMO vm deploy debian --name killtest --memory 512 >/dev/null 2>&1
pass "Deployed killtest"

KILLVMID=$(sqlite3 "$DB" "SELECT id FROM vm WHERE name='killtest' LIMIT 1")
KILLPID=$(cat "$HOME/.sistemo/vms/$KILLVMID/firecracker.pid")
kill "$KILLPID" 2>/dev/null || true
echo "  Waiting 35s for reconciler..."
sleep 35

OUT=$($SISTEMO vm list 2>&1)
assert_contains "$OUT" "stopped" "killtest is stopped after reconciler"

OUT=$($SISTEMO volume list 2>&1)
assert_contains "$OUT" "killtest-root" "killtest-root still exists"
assert_contains "$OUT" "attached" "killtest-root still attached"

$SISTEMO vm start killtest >/dev/null 2>&1
pass "Restarted killtest from root volume"

$SISTEMO vm delete killtest >/dev/null 2>&1

# ─── Test 15: Audit trail ────────────────────────────────
section "15. Audit trail"

OUT=$($SISTEMO history 2>&1)
assert_contains "$OUT" "create" "Audit shows create"
assert_contains "$OUT" "delete" "Audit shows delete"

# ─── Cleanup ─────────────────────────────────────────────
section "Cleanup"
cleanup_all

# ─── Graceful shutdown ───────────────────────────────────
section "16. Graceful shutdown"

kill $DAEMON_PID 2>/dev/null || true
sleep 2
if grep -q "reconciler stopped" /tmp/sistemo-daemon.log; then
    pass "Graceful shutdown (reconciler stopped)"
else
    fail "Reconciler did not stop cleanly"
fi

# ─── Summary ─────────────────────────────────────────────
echo ""
echo "════════════════════════════════════"
echo -e "  ${GREEN}PASSED: $PASS${NC}  ${RED}FAILED: $FAIL${NC}"
echo "════════════════════════════════════"

if [ $FAIL -gt 0 ]; then
    echo -e "${RED}Some tests failed. Check output above.${NC}"
    exit 1
else
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
fi
