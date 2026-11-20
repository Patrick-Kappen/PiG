### Fixed

- `make generate` no longer fails after rendering the help text: the help generator now removes its temporary copy of the protected Pi oracle instead of exiting 1 because it could not delete the copy's read-only directories. The committed help and the shared oracle are unchanged. <!-- fix: 9e74cf693d --> <!-- cause: `cp -R` preserved the oracle's 0555 directory modes in the copy, and the EXIT trap ran `rm -rf` without making them writable first -->
