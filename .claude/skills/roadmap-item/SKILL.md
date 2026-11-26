---
name: roadmap-item
description: Work one item from Odin's ROADMAP.md / docs/AUDIT.md end to end (pick, failing test, fix, update both files, commit, push). Use when asked to fix a finding, continue the roadmap, or close an audit item.
---

# Work a roadmap item

1. **Pick.** Open `ROADMAP.md`. Take the first unchecked item under "Now", or the requested one.
   Items under "Next" say why they wait; anything that changes config keys, admin API routes,
   CLI flags or stored data needs the owner's approval first, so stop and ask.
2. **Understand.** Read the finding in `docs/AUDIT.md`; its location is `file:line` at commit
   `1a66ef4` (`git show 1a66ef4:<file>`). Read the current code: it may have moved.
3. **Failing test.** Write a test that shows the bug (end-to-end tests go in
   `test/unit/pkg/gateway`; see the `service-feature` skill). Run it and confirm it fails:
   `scripts/docker-check.sh -- go test -run TestName ./test/unit/...`.
4. **Fix** the code. Comments say why, and what the old behaviour was.
5. **Gate**: `scripts/docker-check.sh` must end with `All checks passed.`.
6. **Update the records**: tick the item in `ROADMAP.md`, and set the finding's Status in
   `docs/AUDIT.md` to `Fixed <commit>`. After committing, fill in the hash; batching several
   hashes into the next docs commit is fine. Add a line to `CHANGELOG.md` under Unreleased.
7. **Commit and push**: Conventional Commit message explaining the bug and the fix; no
   `Co-Authored-By` or other AI attribution (`git log -1 --format=%B` to check); `git push origin
   main`; confirm CI on the pushed commit.
