# ycb-snap-3c6f87a4

Auto-managed benchmark snapshot repo, created by [YourCodingBench].
Each `task/*` branch is a single orphan commit — that task's SOURCE
repo's tree at the task's base SHA, with no history (tasks in one
benchmark may come from different source repos). Cloud agent attempts
clone these branches. Do not push to this repo; it may be deleted and
rebuilt at any time.

This snapshot repo is **public** because it mirrors public source
repositories only, and the account that owns it granted
YourCodingBench public repository access only. Granting private
repo access in Settings makes future snapshot repos private.
