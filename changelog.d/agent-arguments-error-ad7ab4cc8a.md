### Fixed

- A tool call whose arguments are not a JSON object (`[]`, `42`, `"text"`, `null`, or text that is no JSON) is refused with Pi's `Invalid arguments for tool "<name>": …` result; the tool no longer runs with `{}`. <!-- fix: ad7ab4cc8a -->
