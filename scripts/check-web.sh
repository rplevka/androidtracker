#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temp_js="$(mktemp --suffix=.js)"
trap 'rm -f "$temp_js"' EXIT
python3 - "$project_root/web/index.html" > "$temp_js" <<'PY'
from html.parser import HTMLParser
import sys

class InlineScripts(HTMLParser):
    def __init__(self):
        super().__init__()
        self.inline = False
        self.parts = []

    def handle_starttag(self, tag, attrs):
        if tag == "script" and not dict(attrs).get("src"):
            self.inline = True

    def handle_endtag(self, tag):
        if tag == "script":
            self.inline = False

    def handle_data(self, data):
        if self.inline:
            self.parts.append(data)

parser = InlineScripts()
with open(sys.argv[1], encoding="utf-8") as source:
    parser.feed(source.read())
print("\n".join(parser.parts))
PY
node --check "$temp_js"
