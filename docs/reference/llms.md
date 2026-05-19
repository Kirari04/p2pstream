# LLM-Ready Docs

p2pstream publishes machine-readable documentation files with every docs build. Use them when an AI coding tool needs project docs without crawling the full site.

## Files

| File | Use |
| --- | --- |
| [llms.txt](/llms.txt) | Lightweight documentation index with links to each docs page. |
| [llms-full.txt](/llms-full.txt) | Full Markdown documentation bundle for tools with larger context windows. |

## Copy URLs

Use these URLs in tools that ask for an `llms.txt` source:

```text
https://kirari04.github.io/p2pstream/llms.txt
https://kirari04.github.io/p2pstream/llms-full.txt
```

Or fetch them from a shell:

```bash
curl https://kirari04.github.io/p2pstream/llms.txt
curl https://kirari04.github.io/p2pstream/llms-full.txt
```

## Related Tasks

- [Configuration reference](./configuration)
- [CLI reference](./cli)
