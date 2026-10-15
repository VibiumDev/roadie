"""Shared output for the hardware tests.

The tests grew up separately and each invented its own way of talking:
boxed banners in one, emoji section headers in another, a single summary
line in a third. Run one at a time that is merely inconsistent; run them
all from "make test" and it reads as four unrelated programs.

Everything here is deliberately plain. One line per fact, one shape for
each kind of fact, so a passing run is short and a failure is the thing
that stands out.
"""

import sys

RULE = "─" * 62


def heading(name, summary=""):
    """Announce a test. Every test starts with exactly one of these."""
    print(f"\n▶ {name}" + (f"  ·  {summary}" if summary else ""))


def section(msg):
    """A stage within a test, for the longer ones."""
    print(f"\n  {msg}")


def ok(msg):
    print(f"  ✅ {msg}")


def check(label, detail="", width=18):
    """A single passing assertion: aligned label, then what was seen."""
    print(f"  ✅ {label:<{width}} {detail}".rstrip())


def warn(msg):
    print(f"  ⚠️  {msg}")


def note(msg):
    """Supporting detail, subordinate to whatever was just said."""
    print(f"     {msg}")


def skip(label, why, width=18):
    print(f"  ⏭️  {label:<{width}} {why}")


def result(summary):
    """How a test signs off. The last line of every test."""
    print(f"  ✅ {summary}")


def fail(msg):
    print(f"\n  ❌ {msg}")
    sys.exit(1)


def banner(msg):
    """The suite's own verdict, which should be visible at a glance."""
    print(f"\n{RULE}\n  {msg}\n{RULE}")
