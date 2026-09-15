#!/bin/sh
set -e
cd /tmp
npm install --no-audit --no-fund left-pad
# Compile/run assertion: use the installed package for real.
node -e 'const p=require("left-pad");const s=p("7",3,"0");if(s!=="007"){console.error("bad:",s);process.exit(1)}console.log("left-pad ->",s)'
