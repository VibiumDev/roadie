#!/usr/bin/env python3
"""End-to-end test of the linux-gadget backend with no phone attached.

The kernel's dummy_hcd joins a virtual USB device controller to a virtual
host controller on the same machine. The gadget binds to the virtual
controller and this Pi's own USB host stack enumerates it, with the same
drivers an Android kernel uses: hid-generic and hid-multitouch for input,
cdc_ether for the network. The host side stands in for the phone:

  - input: the test reads the host-side input devices (grabbed, so nothing
    reaches this machine's desktop) while driving roadie's HTTP API;
  - network: the host-side Ethernet interface goes into a network namespace
    and asks roadie-phone-net for DHCP, then tries the internet, DNS, and
    the addresses it must not reach;
  - lifecycle: roadie is restarted, killed and the gadget re-enumerated,
    checking nothing is left held and the phone keeps its network.

dummy_hcd has no isochronous transfers, so the audio function is left out
here; USB audio needs a phone (docs/how-to/linux-gadget.md, checklist).

The Raspberry Pi kernel does not ship dummy_hcd. It is built out of tree
from the kernel's own source, which needs the kernel headers
(linux-headers-rpi-v8) and network access the first time. Needs sudo.

Run from the repository root:  python3 gadget/test_loopback.py
Only the phone network, over a virtual Ethernet pair instead of USB (no
kernel module, no gadget needed):  python3 gadget/test_loopback.py --phone-net-only
Leaves the machine as it found it, including a gadget bound to the real
controller, which is rebound afterwards.
"""

import fcntl
import json
import os
import platform
import re
import select
import signal
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "board"))
from testout import check, fail, heading, note, result, section, skip, warn  # noqa: E402

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
GADGET_SCRIPT = os.path.join(REPO, "gadget", "roadie-gadget")
NET_SCRIPT = os.path.join(REPO, "gadget", "roadie-phone-net")
CONFIGFS = "/sys/kernel/config/usb_gadget/roadie"
NETNS = "roadie-loop"
IFACE = "phone0"  # the Pi's side of the phone link
PORT = 18081
VID, PID = "1d6b", "0104"
RULES = "/run/udev/rules.d/90-roadie-loopback.rules"

EVENT_FORMAT = "llHHi"
EVENT_SIZE = struct.calcsize(EVENT_FORMAT)
EVIOCGRAB = (1 << 30) | (4 << 16) | (ord("E") << 8) | 0x90
EV_KEY, EV_REL, EV_ABS = 0x01, 0x02, 0x03
ABS_X, ABS_Y = 0x00, 0x01
ABS_MT_SLOT, ABS_MT_POSITION_X, ABS_MT_POSITION_Y, ABS_MT_TRACKING_ID = 0x2F, 0x35, 0x36, 0x39
BTN_LEFT, BTN_TOUCH = 0x110, 0x14A
REL_WHEEL = 0x08
KEY_A, KEY_LEFTSHIFT = 30, 42

# Linux keycodes back to characters, to read typed text back.
LOWER = {}
for chars, base in (("1234567890", 2), ("qwertyuiop", 16), ("asdfghjkl", 30), ("zxcvbnm", 44)):
    for i, c in enumerate(chars):
        LOWER[base + i] = c
LOWER.update({57: " ", 12: "-", 13: "=", 51: ",", 52: ".", 53: "/", 39: ";"})
SHIFTED = {"1": "!", "2": "@", "/": "?", ";": ":", "-": "_", ",": "<", ".": ">"}

# Keep the host side of our own gadget off this machine's desktop, out of
# NetworkManager's hands and away from the sound server.
UDEV_RULES = f"""\
SUBSYSTEM=="input", ATTRS{{idVendor}}=="{VID}", ATTRS{{idProduct}}=="{PID}", ENV{{LIBINPUT_IGNORE_DEVICE}}="1", ENV{{ID_INPUT}}="", ENV{{ID_INPUT_KEYBOARD}}="", ENV{{ID_INPUT_MOUSE}}="", ENV{{ID_INPUT_TOUCHSCREEN}}=""
SUBSYSTEM=="net", ATTRS{{idVendor}}=="{VID}", ATTRS{{idProduct}}=="{PID}", ENV{{NM_UNMANAGED}}="1"
SUBSYSTEM=="sound", ATTRS{{idVendor}}=="{VID}", ATTRS{{idProduct}}=="{PID}", ENV{{ACP_IGNORE}}="1", ENV{{PULSE_IGNORE}}="1"
"""

DHCP_SCRIPT = """#!/bin/sh
case "$1" in
  bound|renew)
    ip addr flush dev "$interface"
    ip addr add "$ip/${mask:-24}" dev "$interface"
    ip route replace default via "$router" dev "$interface"
    echo "ip=$ip router=$router dns=$dns" > "$LEASE_OUT"
    ;;
esac
"""


def sudo(*args, check=True, env=None, capture=True):
    cmd = ["sudo", "-n"]
    if env:
        cmd += ["env"] + [f"{k}={v}" for k, v in env.items()]
    cmd += list(args)
    r = subprocess.run(cmd, capture_output=capture, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(args)} failed: {(r.stderr or r.stdout).strip()}")
    return r


def in_ns(*args, timeout=10):
    try:
        return sudo("ip", "netns", "exec", NETNS, *args, check=False)
    except subprocess.TimeoutExpired:
        return None


def wait_for(what, fn, timeout=10.0, interval=0.1):
    deadline = time.time() + timeout
    while time.time() < deadline:
        v = fn()
        if v:
            return v
        time.sleep(interval)
    fail(f"timed out waiting for {what}")


def api(method, path, body=None):
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


# ---- the virtual controller ----------------------------------------------

def build_dummy_hcd():
    release = platform.release()
    build = f"/lib/modules/{release}/build"
    if not os.path.isdir(build):
        fail(f"no kernel headers at {build}; sudo apt install linux-headers-rpi-v8")
    cache = os.path.expanduser(f"~/.cache/roadie/dummy_hcd/{release}")
    ko = os.path.join(cache, "dummy_hcd.ko")
    if os.path.exists(ko):
        return ko
    os.makedirs(cache, exist_ok=True)
    major_minor = ".".join(release.split(".")[:2])
    url = (f"https://raw.githubusercontent.com/raspberrypi/linux/rpi-{major_minor}.y/"
           "drivers/usb/gadget/udc/dummy_hcd.c")
    note(f"building dummy_hcd for {release} from {url}")
    with urllib.request.urlopen(url, timeout=30) as r:
        src = r.read()
    if b"Dummy/Loopback USB host and device emulator" not in src:
        fail("downloaded dummy_hcd.c does not look like the kernel driver")
    with open(os.path.join(cache, "dummy_hcd.c"), "wb") as f:
        f.write(src)
    with open(os.path.join(cache, "Kbuild"), "w") as f:
        f.write("obj-m := dummy_hcd.o\n")
    r = subprocess.run(["make", "-C", build, f"M={cache}", "modules"], capture_output=True, text=True)
    if r.returncode != 0 or not os.path.exists(ko):
        fail("building dummy_hcd failed:\n" + r.stdout[-2000:] + r.stderr[-2000:])
    return ko


def host_usb_device():
    """sysfs dir of the gadget as the host side sees it, on the dummy bus."""
    for d in os.listdir("/sys/bus/usb/devices"):
        p = f"/sys/bus/usb/devices/{d}"
        try:
            if (open(f"{p}/idVendor").read().strip() == VID
                    and open(f"{p}/idProduct").read().strip() == PID
                    and "dummy_hcd" in os.path.realpath(p)):
                return p
        except OSError:
            continue
    return None


def host_inputs():
    """Host-side event devices by gadget interface: 0 keyboard, 1 mouse, 2 touch."""
    found = {}
    for ev in os.listdir("/sys/class/input"):
        if not ev.startswith("event"):
            continue
        real = os.path.realpath(f"/sys/class/input/{ev}/device")
        if "dummy_hcd" not in real:
            continue
        m = re.search(r":1\.(\d+)/", real)
        if m:
            found[int(m.group(1))] = f"/dev/input/{ev}"
    return found


def host_netdev():
    for n in os.listdir("/sys/class/net"):
        if "dummy_hcd" in os.path.realpath(f"/sys/class/net/{n}"):
            return n
    return None


# ---- reading input -------------------------------------------------------

class Input:
    """A grabbed input device: events go to this test only."""

    def __init__(self, path):
        self.path = path
        # A node that has just appeared is root's until udev gets to it.
        wait_for(f"{path} to become readable", lambda: os.access(path, os.R_OK), timeout=5)
        self.fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
        fcntl.ioctl(self.fd, EVIOCGRAB, 1)
        self.drain()

    def read(self, quiet=0.3, timeout=3.0):
        """Events until none arrive for `quiet` seconds."""
        events, deadline, last = [], time.time() + timeout, time.time()
        while time.time() < deadline and time.time() - last < quiet:
            r, _, _ = select.select([self.fd], [], [], 0.05)
            if not r:
                continue
            data = os.read(self.fd, EVENT_SIZE * 64)
            for i in range(0, len(data), EVENT_SIZE):
                _, _, typ, code, value = struct.unpack(EVENT_FORMAT, data[i:i + EVENT_SIZE])
                if typ:
                    events.append((typ, code, value))
            last = time.time()
        return events

    def drain(self):
        self.read(quiet=0.1, timeout=0.5)

    def during(self, fn):
        """Events produced while fn runs. Read concurrently: evdev keeps only
        a few dozen events per reader, and typing a sentence makes hundreds,
        so reading afterwards would find the start already dropped."""
        box = {}
        t = threading.Thread(target=lambda: box.update(r=fn()))
        t.start()
        events = []
        while t.is_alive():
            events += self.read(quiet=0.05, timeout=0.2)
        t.join()
        events += self.read()
        return box.get("r"), events

    def close(self):
        try:
            fcntl.ioctl(self.fd, EVIOCGRAB, 0)
        except OSError:
            pass
        os.close(self.fd)


def typed_text(events):
    out, shift = "", False
    for typ, code, value in events:
        if typ != EV_KEY:
            continue
        if code == KEY_LEFTSHIFT:
            shift = value != 0
        elif value == 1 and code in LOWER:
            c = LOWER[code]
            out += (SHIFTED.get(c, c.upper()) if shift else c)
    return out


def last_value(events, typ, code):
    v = None
    for t, c, val in events:
        if t == typ and c == code:
            v = val
    return v


def slot_tracking(events):
    """Each multitouch slot's last tracking id (-1 = lifted)."""
    slot, ids = 0, {}
    for t, c, v in events:
        if t != EV_ABS:
            continue
        if c == ABS_MT_SLOT:
            slot = v
        elif c == ABS_MT_TRACKING_ID:
            ids[slot] = v
    return ids


# ---- roadie --------------------------------------------------------------

class Roadie:
    def __init__(self, binary, logpath):
        self.binary, self.logpath, self.proc = binary, logpath, None

    def start(self):
        self.log = open(self.logpath, "a")
        # --source: no capture dongle needed or touched; video is not under test.
        self.proc = subprocess.Popen(
            [self.binary, "--hid-backend", "linux-gadget", "--port", str(PORT), "--name", "loopback",
             "--source", "http://127.0.0.1:9/raw-stream"],
            stdout=self.log, stderr=subprocess.STDOUT)
        wait_for("roadie to report the HID connected",
                 lambda: self._status().get("status") == "connected", timeout=15)

    def _status(self):
        try:
            return json.loads(api("GET", "/api/hid/status")[1])
        except Exception:
            return {}

    def status(self):
        return self._status()

    def stop(self, sig=signal.SIGTERM):
        if self.proc and self.proc.poll() is None:
            self.proc.send_signal(sig)
            self.proc.wait(timeout=15)
        self.proc = None


def main():
    heading("test-gadget-loopback", "the gadget through a real Linux host stack")
    if not os.path.exists(GADGET_SCRIPT):
        fail("run from the repository root")
    if sudo("true", check=False).returncode != 0:
        fail("needs passwordless sudo (sudo -n true)")

    tmp = tempfile.mkdtemp(prefix="roadie-loopback-")
    binary = os.path.join(tmp, "roadie")
    r = subprocess.run(["go", "build", "-o", binary, "."], cwd=REPO, capture_output=True, text=True)
    if r.returncode != 0:
        fail("go build failed:\n" + r.stderr)

    group = os.popen("id -gn").read().strip()
    if "roadie" in os.popen("id -Gn").read().split():
        group = "roadie"
    conf = os.path.join(tmp, "gadget.conf")
    with open(conf, "w") as f:
        f.write(f"UDC=dummy_udc.0\nAUDIO=none\nGADGET_GROUP={group}\n")
    netconf = os.path.join(tmp, "phone-net.conf")
    upstream = os.popen("ip -4 route show default | awk '{print $5; exit}'").read().strip() or "eth0"
    with open(netconf, "w") as f:
        f.write(f"UPSTREAM_IF={upstream}\n")
    dhcp_script = os.path.join(tmp, "udhcpc.sh")
    with open(dhcp_script, "w") as f:
        f.write(DHCP_SCRIPT)
    os.chmod(dhcp_script, 0o755)
    lease_out = os.path.join(tmp, "lease")

    real_gadget = None
    if os.path.isdir(CONFIGFS):
        real_gadget = open(f"{CONFIGFS}/UDC").read().strip()
    ko = build_dummy_hcd()

    roadie = Roadie(binary, os.path.join(tmp, "roadie.log"))
    dnsmasq = None
    inputs = []
    try:
        section("Set up")
        sudo("mkdir", "-p", os.path.dirname(RULES))
        subprocess.run(["sudo", "-n", "tee", RULES], input=UDEV_RULES, text=True,
                       capture_output=True, check=True)
        sudo("udevadm", "control", "--reload")
        if real_gadget is not None:
            sudo(GADGET_SCRIPT, "down")
            note(f"took the gadget off {real_gadget or 'no controller'} for the test")
        # Several virtual controllers. After dummy_hcd has been unloaded and
        # reloaded, registering a network interface on one of its devices can
        # fail in the driver core ("kobject_add_internal failed ... parent:
        # net": a stale sysfs glue directory at a reused address). The real
        # controller is never unloaded and is not affected; here, the first
        # instance that binds is used. A reboot clears it entirely.
        sudo("insmod", ko, "num=4")
        for udc in ("dummy_udc.0", "dummy_udc.1", "dummy_udc.2", "dummy_udc.3"):
            with open(conf, "w") as f:
                f.write(f"UDC={udc}\nAUDIO=none\nGADGET_GROUP={group}\n")
            if sudo(GADGET_SCRIPT, "up", env={"ROADIE_GADGET_CONF": conf}, check=False).returncode == 0:
                break
        else:
            fail("the gadget would not bind to any virtual controller; see dmesg (a reboot clears it)")
        usb = wait_for("the host side to enumerate the gadget", host_usb_device)
        devs = wait_for("keyboard, mouse and touch input devices",
                        lambda: (lambda d: d if len(d) >= 3 else None)(host_inputs()))
        check("enumerated", f"{os.path.basename(usb)}: keyboard {devs[0]}, mouse {devs[1]}, touch {devs[2]}")
        drivers = {i: os.path.basename(os.path.realpath(
            f"{usb}/{os.path.basename(usb)}:1.{i}/driver")) for i in range(3)}
        check("HID drivers", ", ".join(f"if{i} {d}" for i, d in drivers.items()))
        if drivers[2] != "usbhid":
            warn(f"touch interface bound to {drivers[2]}")
        hid_drv = [os.path.basename(os.path.realpath(p + "/driver"))
                   for p in (f"{usb}/{os.path.basename(usb)}:1.2/" + d
                             for d in os.listdir(f"{usb}/{os.path.basename(usb)}:1.2")
                             if d.startswith("0003:"))]
        if "hid-multitouch" not in hid_drv:
            fail(f"touch interface not claimed by hid-multitouch ({hid_drv})")
        check("touchscreen", "claimed by hid-multitouch, the driver Android uses")

        section("Network: DHCP, DNS, NAT and isolation")
        sudo(NET_SCRIPT, "prepare", "phone0", env={"ROADIE_PHONE_NET_CONF": netconf})
        dnsmasq = subprocess.Popen(["sudo", "-n", "env", f"ROADIE_PHONE_NET_CONF={netconf}",
                                    NET_SCRIPT, "run", "phone0"],
                                   stdout=open(os.path.join(tmp, "dnsmasq.log"), "w"),
                                   stderr=subprocess.STDOUT)
        time.sleep(1)
        lease = join_phone_network(dhcp_script, lease_out)
        check("DHCP", lease)
        if "ip=192.168.42.2 " not in lease + " ":
            fail(f"phone did not get its fixed address: {lease}")
        network_checks(upstream)
        closed_checks(upstream, netconf)

        section("Input through roadie's API")
        roadie.start()
        st = roadie.status()
        check("status", f"{st.get('status')}, usb_state {st.get('usb_state')}, backend {st.get('backend')}")
        if st.get("usb_state") != "configured":
            fail(f"/api/hid/status: {st}")
        kbd, mouse, touch = Input(devs[0]), Input(devs[1]), Input(devs[2])
        inputs = [kbd, mouse, touch]
        input_checks(kbd, mouse, touch)

        section("Lifecycle")
        devnum = open(f"{usb}/devnum").read().strip()
        roadie.stop()
        time.sleep(1)
        if host_usb_device() != usb or open(f"{usb}/devnum").read().strip() != devnum:
            fail("stopping roadie re-enumerated the gadget")
        r = in_ns("ping", "-c", "1", "-W", "2", "192.168.42.1")
        if r is None or r.returncode != 0:
            fail("the phone lost its network while roadie was stopped")
        check("roadie stopped", "gadget still enumerated, phone network still up")

        roadie.start()
        api("POST", "/api/hid/touch", {"contacts": [{"id": 0, "tip": True, "x": 9000, "y": 9000}]})
        if slot_tracking(touch.read()).get(0, -1) < 0:
            fail("finger did not go down")
        roadie.stop(signal.SIGKILL)  # no chance to let go
        roadie.start()
        ev = touch.read(quiet=0.5, timeout=3)
        if slot_tracking(ev).get(0) != -1:
            fail(f"finger held by a killed roadie was not lifted on restart ({ev})")
        check("roadie killed", "the finger it held was lifted when it came back")

        for i in inputs:
            i.close()
        inputs = []
        # dummy_hcd cannot come back from a soft disconnect (it fails the
        # same way with a one-function gadget), so the replug here is the
        # harder one: unbinding the controller, which also recreates the
        # HID nodes roadie has open. "roadie-gadget reset" (soft_connect)
        # is checked against a phone; see the hardware checklist.
        sudo(GADGET_SCRIPT, "rebind", env={"ROADIE_GADGET_CONF": conf})
        wait_for("the gadget to re-enumerate",
                 lambda: host_usb_device() and len(host_inputs()) >= 3 and host_netdev(), timeout=15)
        wait_for("roadie to see the phone again",
                 lambda: roadie.status().get("usb_state") == "configured", timeout=10)
        devs = host_inputs()
        kbd = Input(devs[0])
        inputs = [kbd]
        (code, body), ev = kbd.during(lambda: api("POST", "/api/hid/type", {"text": "back"}))
        if code != 200 or typed_text(ev) != "back":
            fail(f"typing after re-enumeration: {code} {body} {typed_text(ev)!r}")
        # A replug gives the phone a new network interface; it asks again.
        sudo("ip", "netns", "del", NETNS, check=False)
        lease = join_phone_network(dhcp_script, lease_out)
        if "ip=192.168.42.2 " not in lease + " ":
            fail(f"after re-enumeration: {lease}")
        check("re-enumerated", "roadie reopened the new nodes; input works and the phone got its address back")

        result("gadget works end to end through a Linux host (audio not covered: needs a phone)")
    finally:
        for i in inputs:
            try:
                i.close()
            except OSError:
                pass
        roadie.stop()
        if dnsmasq:
            # The bracket keeps pkill's pattern from matching its own sudo.
            sudo("pkill", "-f", "dnsmas[q] .*roadie-phone-net/phone0", check=False)
            dnsmasq.wait(timeout=10)
        sudo(NET_SCRIPT, "cleanup", "phone0", env={"ROADIE_PHONE_NET_CONF": netconf}, check=False)
        sudo(NET_SCRIPT, "remove", check=False)
        sudo("ip", "netns", "del", NETNS, check=False)
        sudo(GADGET_SCRIPT, "down", env={"ROADIE_GADGET_CONF": conf}, check=False)
        time.sleep(2)  # let the interface's sysfs entries go before the devices do
        sudo("rmmod", "dummy_hcd", check=False)
        sudo("rm", "-f", RULES, check=False)
        sudo("udevadm", "control", "--reload", check=False)
        if real_gadget:
            restore = {"ROADIE_GADGET_CONF": os.environ["ROADIE_GADGET_CONF"]} \
                if "ROADIE_GADGET_CONF" in os.environ else None
            r = sudo(GADGET_SCRIPT, "up", env=restore, check=False)
            if r.returncode != 0:
                warn("could not rebind the gadget to " + real_gadget + ": " + r.stderr.strip())
                note("ROADIE_GADGET_CONF may be needed: sudo gadget/roadie-gadget up")
        note(f"logs in {tmp}")


def join_phone_network(dhcp_script, lease_out, dev=None):
    """Move the host side's Ethernet into the phone's namespace and DHCP.
    dev: an interface already in the namespace (the veth peer)."""
    if dev is None:
        dev = wait_for("the host-side Ethernet interface", host_netdev)
        sudo("ip", "netns", "add", NETNS, check=False)
        sudo("ip", "link", "set", dev, "netns", NETNS)
    in_ns("ip", "link", "set", "lo", "up")
    in_ns("ip", "link", "set", dev, "up")
    if os.path.exists(lease_out):
        os.remove(lease_out)
    sudo("ip", "netns", "exec", NETNS, "env", f"LEASE_OUT={lease_out}", "busybox", "udhcpc",
         "-i", dev, "-n", "-q", "-t", "8", "-T", "1", "-s", dhcp_script)
    return open(lease_out).read().strip()


def network_checks(upstream):
    def ok(*cmd):
        r = in_ns(*cmd)
        return r is not None and r.returncode == 0

    if not ok("ping", "-c", "1", "-W", "2", "192.168.42.1"):
        fail("phone cannot ping its gateway")
    check("gateway", "192.168.42.1 answers ping")
    pi_has_internet = subprocess.run(["ping", "-c", "1", "-W", "2", "1.1.1.1"],
                                     capture_output=True).returncode == 0
    if pi_has_internet:
        if not ok("busybox", "nslookup", "example.com", "192.168.42.1"):
            fail("DNS through the Pi failed")
        check("DNS", "example.com resolves through the Pi")
        if not ok("ping", "-c", "1", "-W", "3", "1.1.1.1"):
            fail(f"no internet through {upstream}")
        if not ok("busybox", "wget", "-q", "-T", "5", "-O", "/dev/null",
                  "http://connectivitycheck.gstatic.com/generate_204"):
            fail("HTTP to the internet failed")
        check("internet", f"ping and HTTP out through {upstream} (NAT)")
    else:
        skip("internet", "this Pi has no internet to share")

    lan_gw = os.popen(f"ip -4 route show default dev {upstream} | awk '{{print $3; exit}}'").read().strip()
    lan_ip = os.popen(f"ip -4 -o addr show dev {upstream} | awk '{{print $4}}' | cut -d/ -f1").read().strip()
    def tcp(host, port):
        return ("timeout", "3", "bash", "-c", f"echo > /dev/tcp/{host}/{port}")

    blocked = [("Pi's SSH on the phone link", tcp("192.168.42.1", 22)),
               ("Pi's SSH at its LAN address", tcp(lan_ip, 22)),
               (f"roadie's port {PORT}", tcp("192.168.42.1", PORT))]
    if lan_gw:
        blocked.append((f"LAN router {lan_gw}", ("ping", "-c", "1", "-W", "2", lan_gw)))
    for what, cmd in blocked:
        if ok(*cmd):
            fail(f"phone reached {what}; it must not")
    check("isolation", "no SSH, no roadie, no LAN: " + ", ".join(w for w, _ in blocked))
    fwd = {k: os.popen(f"sysctl -n net.ipv4.conf.{k}.forwarding").read().strip()
           for k in ("all", upstream, IFACE)}
    if fwd["all"] != "0":
        warn(f"net.ipv4.conf.all.forwarding is {fwd['all']} (something else enabled it)")
    check("forwarding", ", ".join(f"{k}={v}" for k, v in fwd.items()) + " (per interface only)")


def closed_checks(upstream, netconf):
    """While roadie-phone-net is stopped or restarting, the phone -- which
    keeps its lease -- must be shut out, not let through."""
    rules = sudo("nft", "list", "table", "inet", "roadie_phone").stdout
    if f'iifname "{upstream}" oifname != "{IFACE}"' not in rules:
        fail("no guard against routing the LAN through this Pi")
    check("router guard", f"nothing forwarded from {upstream} except to the phone")
    socks = os.popen("ss -H -lnu sport = :53").read()
    if "127.0.0.1:53" in socks or "[::1]:53" in socks:
        if "dnsmasq" in sudo("ss", "-H", "-lnup", "sport", "=", ":53").stdout:
            fail("roadie's dnsmasq is listening on loopback")
    check("dnsmasq", "listens on the phone link only")

    lan_ip = os.popen(f"ip -4 -o addr show dev {upstream} | awk '{{print $4}}' | cut -d/ -f1").read().strip()
    sudo(NET_SCRIPT, "cleanup", IFACE, env={"ROADIE_PHONE_NET_CONF": netconf})
    for what, cmd in (("the Pi's SSH at its LAN address", ("timeout", "3", "bash", "-c", f"echo > /dev/tcp/{lan_ip}/22")),
                      ("the internet", ("ping", "-c", "1", "-W", "2", "1.1.1.1")),
                      ("the Pi on the phone link", ("ping", "-c", "1", "-W", "2", "192.168.42.1"))):
        r = in_ns(*cmd)
        if r is not None and r.returncode == 0:
            fail(f"with the service stopped, the phone reached {what}")
    check("stopped", "the phone, still holding its lease, reaches nothing")
    sudo(NET_SCRIPT, "prepare", IFACE, env={"ROADIE_PHONE_NET_CONF": netconf})
    r = in_ns("ping", "-c", "1", "-W", "3", "192.168.42.1")
    if r is None or r.returncode != 0:
        fail("phone link did not come back after prepare")
    check("restarted", "back to normal after prepare")


def input_checks(kbd, mouse, touch):
    text = "Roadie gadget, typing a longer sentence: 1 2 3!"
    (code, _), ev = kbd.during(lambda: api("POST", "/api/hid/type", {"text": text}))
    got = typed_text(ev)
    if code != 200 or got != text:
        fail(f"typed {got!r}")
    check("type", repr(got))

    api("POST", "/api/hid/key", {"keycode": 4, "action": "press"})
    ev = kbd.read()
    api("POST", "/api/hid/key", {"keycode": 4, "action": "release"})
    ev += kbd.read()
    if (EV_KEY, KEY_A, 1) not in ev or (EV_KEY, KEY_A, 0) not in ev:
        fail(f"key press/release: {ev}")
    check("key", "press and release of 'a'")

    api("POST", "/api/hid/mouse/move", {"x": 16384, "y": 8192})
    ev = mouse.read()
    if last_value(ev, EV_ABS, ABS_X) != 16384 or last_value(ev, EV_ABS, ABS_Y) != 8192:
        fail(f"mouse move: {ev}")
    api("POST", "/api/hid/mouse/move", {"x": 16384, "y": 8192})
    ev = mouse.read()
    if last_value(ev, EV_ABS, ABS_X) != 16384:
        fail(f"a repeated move went nowhere: {ev}")
    api("POST", "/api/hid/mouse/click", {"buttons": 1})
    ev = mouse.read()
    if (EV_KEY, BTN_LEFT, 1) not in ev or (EV_KEY, BTN_LEFT, 0) not in ev:
        fail(f"click: {ev}")
    api("POST", "/api/hid/mouse/scroll", {"amount": 2})
    ev = mouse.read()
    if last_value(ev, EV_REL, REL_WHEEL) != -2:
        fail(f"scroll: {ev}")
    check("mouse", "absolute move, repeated move, click, scroll")

    api("POST", "/api/hid/touch", {"contacts": [
        {"id": 0, "tip": True, "x": 8000, "y": 9000}, {"id": 1, "tip": True, "x": 20000, "y": 21000}]})
    ev = touch.read()
    ids = slot_tracking(ev)
    if not (ids.get(0, -1) >= 0 and ids.get(1, -1) >= 0):
        fail(f"two fingers down: {ev}")
    xs = sorted(v for t, c, v in ev if t == EV_ABS and c == ABS_MT_POSITION_X)
    if xs != [8000, 20000]:
        fail(f"finger positions {xs}")
    api("POST", "/api/hid/touch", {"contacts": [
        {"id": 0, "tip": False, "x": 8000, "y": 9000}, {"id": 1, "tip": False, "x": 20000, "y": 21000}]})
    ev = touch.read()
    if slot_tracking(ev) != {0: -1, 1: -1} or last_value(ev, EV_KEY, BTN_TOUCH) != 0:
        fail(f"two fingers up: {ev}")
    check("multi-touch", "two contacts down at the right places, both lifted")

    api("POST", "/api/hid/touch", {"contacts": [{"id": 0, "tip": True, "x": 100, "y": 100}]})
    touch.read()
    code, _ = api("POST", "/api/hid/release")
    ev = touch.read()
    if code != 200 or slot_tracking(ev).get(0) != -1:
        fail(f"/api/hid/release left a finger down: {ev}")
    check("release", "/api/hid/release lifts a held finger")


def phone_net_only():
    """The phone network over a veth pair: rphone0 here, vphone in the
    phone's namespace. Everything roadie-phone-net does, minus USB."""
    global IFACE
    IFACE = "rphone0"
    heading("test-phone-net", "DHCP, DNS, NAT and isolation on a virtual phone link")
    if sudo("true", check=False).returncode != 0:
        fail("needs passwordless sudo (sudo -n true)")
    tmp = tempfile.mkdtemp(prefix="roadie-phone-net-")
    upstream = os.popen("ip -4 route show default | awk '{print $5; exit}'").read().strip() or "eth0"
    netconf = os.path.join(tmp, "phone-net.conf")
    with open(netconf, "w") as f:
        # No gadget to pin the lease to: the phone gets one from the range.
        f.write(f"UPSTREAM_IF={upstream}\nGADGET=/nonexistent\n")
    dhcp_script = os.path.join(tmp, "udhcpc.sh")
    with open(dhcp_script, "w") as f:
        f.write(DHCP_SCRIPT)
    os.chmod(dhcp_script, 0o755)
    lease_out = os.path.join(tmp, "lease")
    dnsmasq = None
    try:
        sudo("ip", "netns", "add", NETNS)
        sudo("ip", "link", "add", IFACE, "type", "veth", "peer", "name", "vphone", "netns", NETNS)
        sudo(NET_SCRIPT, "prepare", IFACE, env={"ROADIE_PHONE_NET_CONF": netconf})
        dnsmasq = subprocess.Popen(["sudo", "-n", "env", f"ROADIE_PHONE_NET_CONF={netconf}",
                                    NET_SCRIPT, "run", IFACE],
                                   stdout=open(os.path.join(tmp, "dnsmasq.log"), "w"),
                                   stderr=subprocess.STDOUT)
        time.sleep(1)
        lease = join_phone_network(dhcp_script, lease_out, dev="vphone")
        if not re.search(r"ip=192\.168\.42\.\d+ router=192\.168\.42\.1 dns=192\.168\.42\.1", lease):
            fail(f"DHCP: {lease}")
        check("DHCP", lease)
        network_checks(upstream)
        closed_checks(upstream, netconf)
        result("phone network: served, isolated, and closed whenever it is not running")
    finally:
        if dnsmasq:
            sudo("pkill", "-f", f"dnsmas[q] .*roadie-phone-net/{IFACE}", check=False)
            dnsmasq.wait(timeout=10)
        sudo(NET_SCRIPT, "cleanup", IFACE, env={"ROADIE_PHONE_NET_CONF": netconf}, check=False)
        sudo(NET_SCRIPT, "remove", check=False)
        sudo("ip", "netns", "del", NETNS, check=False)
        sudo("ip", "link", "del", IFACE, check=False)
        note(f"logs in {tmp}")


if __name__ == "__main__":
    if "--phone-net-only" in sys.argv[1:]:
        phone_net_only()
    else:
        main()
