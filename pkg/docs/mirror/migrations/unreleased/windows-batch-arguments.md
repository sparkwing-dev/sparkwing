## Windows batch arguments

Windows commands that resolve to `.cmd` or `.bat` now run as
`cmd.exe /e:ON /v:OFF /d /c` from the system directory with each argument quoted
and escaped for cmd.exe, following Rust's fix for CVE-2024-24576: an argument is
quoted unless it is nonempty, does not end with `\`, and uses only letters, digits,
non-ASCII text and `#$*+-./:?@\_`, and `%` is written as `%%cd:~,%`. `&`, `|`,
`<`, `>`, `^`, `(`, `)`, `!` and `%VAR%` reach the batch file literally. A `"` is passed
as `""`, which programs using the Microsoft C runtime, such as `node.exe` behind
`npm.cmd`, read back as one quote. Arguments containing CR, LF or NUL are refused,
as are batch file paths containing `"`, `^` or `%`; the error identifies the
`argv` position. Use a non-batch executable, such as `node.exe` with the tool's
JavaScript entrypoint, to pass those values. A batch file that enables delayed
expansion or expands its arguments without quotes reparses them itself.

Executable lookup no longer implicitly searches the Windows working directory.
Install tools on PATH or use an explicit path such as `.\tool.exe`. Empty Windows
PATH entries are skipped. A `.` PATH entry still explicitly selects the working
directory.
