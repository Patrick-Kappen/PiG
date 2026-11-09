### Changed

- Where bash is not at `/bin/bash`, as on NixOS, PiG finds it on PATH itself instead of starting `which bash` on every start with extensions and for the bash tool: about 0.5 ms less per start. A bash without the execute bit (Termux) is still found through `which`, as in Pi (D234).
