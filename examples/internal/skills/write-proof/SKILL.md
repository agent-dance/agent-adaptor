---
name: write-proof
description: When explicitly asked to use write-proof, create the requested proof file with the exact content from the prompt.
---

# Write Proof

Use this skill only when the prompt explicitly tells you to use `write-proof`.

## Steps

1. Find the target file path in the prompt.
2. Find the exact required file contents in the prompt.
3. Create or overwrite only that file.
4. Do not modify any other files.
5. Report the path you wrote.
