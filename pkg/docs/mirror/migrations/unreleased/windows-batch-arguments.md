## Windows batch arguments

Windows commands that resolve to `.cmd` or `.bat` now refuse arguments containing
`&`, `|`, `<`, `>`, `^`, `%`, `"`, `!`, `(`, `)` or CR/LF. cmd.exe interprets these
characters even when a pipeline passes an argv array. The error identifies the
`argv` position. Use a non-batch executable, such as `node.exe` with the tool's
JavaScript entrypoint, to pass arbitrary branch names or other untrusted values.

Executable lookup no longer implicitly searches the Windows working directory.
Install tools on PATH or use an explicit path such as `.\tool.exe`. Empty Windows
PATH entries are skipped. A `.` PATH entry still explicitly selects the working
directory.
