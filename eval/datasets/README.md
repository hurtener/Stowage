# Eval datasets

Source URLs, licenses, and checksum notes for all evaluation datasets.

## LongMemEval

- **Source:** https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned  
  and https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned-v2
- **License:** MIT (verified from https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned, repository README — MIT License header present; original paper: Wu et al. 2024, "LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory")
- **Format:** JSON (array of conversation objects with QA pairs)
- **Checksums:** pinned at fetch time; stored in eval/data/.checksums (gitignored with eval/data/)
- **Data location:** eval/data/longmemeval/ (gitignored — never committed)

## LoCoMo

- **Source:** https://github.com/snap-research/locomo  
  File: data/locomo10.json (raw: https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json)
- **License:** MIT (verified from https://github.com/snap-research/locomo/blob/main/LICENSE — MIT License)
- **Format:** JSON (10 multi-session conversations with QA + event annotations)
- **Checksums:** pinned at fetch time; stored in eval/data/.checksums (gitignored)
- **Data location:** eval/data/locomo/ (gitignored)

## ConvoMem / MemBench

Availability not confirmed as of Phase 13 (2026-06-11). Runners ship behind a
dataset-presence check (`SKIPPED: dataset not present`). Confirming and licensing
them is a follow-up (D-055). The binding public suite at launch is LongMemEval +
LoCoMo until then.
