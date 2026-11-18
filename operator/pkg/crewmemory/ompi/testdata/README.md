The files are examples from the OMPI exchange-and-runtime working spec v0.6
(editors' draft of 2026-11-09; spec text is CC BY 4.0). `appendix-a-memory.json`
is the example memory at the top of the spec, `appendix-a-manifest.json` the
manifest example in Notes D.1 (the working text, which still reads
`protocol_version` 0.5), and `appendix-b-memory.json` the smallest conforming
memory (Appendix B, in interchange form). The tests check that the Kubemoot
schemas accept them, and reject memories that break the spec's tiers.
