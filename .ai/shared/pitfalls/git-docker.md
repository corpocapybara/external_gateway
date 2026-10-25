# Git pitfalls

## UTF-16 source files merge as "binary"
- With `* text=auto`, NUL-heavy UTF-16 files are classified binary: `git diff` shows `Bin x -> y bytes` and a
  merge/rebase fails with `Cannot merge binary files`. Cause: the file is UTF-16 on one side, UTF-8 on the other.
- Match origin's encoding per file; never blanket-convert (the mismatch can run both directions).
- Normalizing the tip does not fix a rebase: the merge base comes from the parent commit in the replay, so UTF-16
  in branch history keeps poisoning it. A merge sidesteps this; a clean rebase needs a history rewrite.
- A `Bin x -> y bytes` diff reappearing means an editor re-saved the file as UTF-16. Convert it back first.

## Shared checkout: uncommitted edits can vanish
- If the user or an IDE works in the same checkout concurrently, their `checkout`/`reset` silently reverts files
  you are mid-edit on. Nothing errors.
- Commit right after editing, before building, verifying or investigating further.
- If a file reverted or the branch changed unexpectedly, read `git reflog`. `checkout`/`reset` entries you did not
  make mean another actor is using the checkout: stop and ask before redoing work there.
- When finished, check out the branch the user was on, so the checkout is not left on your throwaway branch.
