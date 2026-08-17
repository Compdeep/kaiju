# Versioning

When someone asks what version is running, there are two answers and they are not
the same number. The engine has its own lineage and the product built on it has
its own release number, and neither tracks the other.

That split is SunOS's. Solaris 11 runs on kernel SunOS 5.11: the marketed number
moves for marketing reasons, the kernel number moves when the kernel changes, and
`uname -r` answers a different question from `/etc/release`. Anyone who has
debugged one from the other knows why both exist.

Here:

    VERSION            0.4            this engine's own number
    Enbarr's VERSION   0.0.2          one product built on it

## The engine's number

Two parts, `<lineage>.<release>`.

The lineage is 0 and does not move for a release. It moves when the engine stops
being the same engine — a change an embedding application cannot absorb by
reading a changelog, where the answer is to port rather than to upgrade. It is
still 0 because that moment has not come.

The release moves whenever the engine ships: 0.4, 0.5, 0.6. A new handler, a
deleted setter, a stage rewritten — all of it is a release, because an
application reads the engine's surface and any of those can reach it.

The number before this file existed was 0.3.0, hardcoded in the Makefile as
`VERSION?=0.3.0`. This continues that count rather than restarting it: 0.4 is the
release after 0.3.0, and the third part is dropped for the reason below.

There is no third part. A patch number invites the question of what is a patch
and what is a release, and the answer would be argued each time.

## What a binary records

When a binary is built, it records both numbers, because a binary contains both
halves and only one of them used to be identifiable.

    version=0.0.2 engine=0.4+816fb5d

The commit after the plus is the engine's build identity, not part of its
version — which build of 0.4 this is. When the checkout it was built from had edits that were never committed,
the commit carries `-dirty` and both the build and the binary say so at startup:
such a binary matches no commit, so nothing can rebuild it.

## Who reads this file

`Enbarr/OmamoriNet/build.sh` reads it and stamps `version.Kaiju`. `cmd/kaiju`
stamps its own `version` variable from it. Nothing reads it at run time — a
version is decided when a binary is made, not while it runs.
