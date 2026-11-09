### Changed

- PiG finds bash once when it starts instead of on every start with extensions and before every bash command, and where bash is not at `/bin/bash`, as on NixOS, it finds it on PATH itself instead of starting `which bash`: about 0.5 ms less per start and per bash command. A bash without the execute bit (Termux) is still found through `which`, as in Pi (D234).
