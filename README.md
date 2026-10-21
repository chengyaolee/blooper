# blooper
Making agents work more with less

## Browser tool setup

The `browser` tool drives Chrome through [Scrapling](https://github.com/D4Vinci/Scrapling)'s MCP server (stealth fingerprints, Cloudflare solver).

```sh
python3.12 -m venv ~/.venvs/scrapling      # any Python 3.10+
~/.venvs/scrapling/bin/pip install "scrapling[ai]"
export SCRAPLING_MCP_BIN=~/.venvs/scrapling/bin/scrapling-mcp   # or put scrapling-mcp on PATH
```

Requires Google Chrome installed locally. Without `scrapling-mcp` the tool returns an install hint and `TestBrowserDetection` is skipped.
