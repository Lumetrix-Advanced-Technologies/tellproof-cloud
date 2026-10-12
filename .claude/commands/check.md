Run `make check` and get it green.

Fix formatting, vet and staticcheck findings directly. For a failing vector, fix the implementation, never the vector. For a govulncheck finding, upgrade the affected module if it is on the allowed list; otherwise report it. Finish with the list of what you changed and the final `make check` result.
