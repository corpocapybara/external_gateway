# Windows shell pitfalls (PowerShell 5.1, Git Bash, encoding)

Complements the global CLAUDE.md (PowerShell UTF-8, Python cp1250, Bash `\\`, here-strings). Not repeated here.

## PowerShell
- Never assume bash syntax, Linux path separators or GNU utilities. Check which shell you are in before writing a command.
- 5.1 has no `&&`. Use `cmd1; if ($?) { cmd2 }`.
- Quote paths with spaces. Use `-LiteralPath`, not `-Path`, for user-controlled paths.
- Piping a string to an external process mangles non-ASCII (`ø` -> `??`) even when the string is correct. Write a
  temp file (`Set-Content -Encoding utf8`) and pass the file to the process.
- `\"` escapes inside a `foreach` body build malformed JSON (literal backslash-quote). Build the body with a
  hashtable and `ConvertTo-Json -Compress`, or make the calls one by one outside the loop.

## Python subprocess and stdout
- A print of a character the console codec cannot encode raises `UnicodeEncodeError` and kills the script
  mid-loop; the output so far looks like a complete answer. Reconfigure stdout once, ASCII-fold at the boundary
  (`s.encode("ascii", "replace").decode()`), or build the report in a list and encode once at the end.
- Capturing a child's output: set the CHILD's encoding (`env["PYTHONIOENCODING"] = "utf-8"`), then decode as
  UTF-8. Reconfiguring the parent's stdout does not change what the child writes.
- With `errors="replace"` a wrong child encoding never raises: bytes become U+FFFD silently. That loss is
  permanent (re-run, don't repair), and it hides content from regex detectors, so a zero-hit scan proves
  nothing until the U+FFFD count is checked.
- Count U+FFFD by parsing JSON, not grepping: `json.dumps` defaults to `ensure_ascii=True`, so it is stored as
  an escape.

## UTF-16 files
- Read and convert with `[IO.File]::ReadAllBytes` + `Encoding.Unicode.GetString(b, 2, ...)` -> `UTF8Encoding($false)`,
  never `Get-Content`.
