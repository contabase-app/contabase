# Fixture histórica de proveniência da Beta 6

Estes cinco arquivos foram extraídos, sem alterações, dos objetos Git da revisão
`975e35b187528411e5f43b6ff066bbdccfc5733e`, de 2026-10-30. O `VERSION` desse
snapshot é `v1.0.0-beta.6`. Os quatro SHA-256 de fontes correspondem aos valores
já fixados em `scripts/lib/contabase-recovery.sh`; a política não foi ampliada.

A suíte lê estes bytes locais: não consulta rede, tags, histórico Git ou o
`VERSION` do checkout em execução. Uma fixture ausente ou divergente deve falhar.
O diretório `testdata` mantém estes fontes históricos fora dos pacotes Go da
aplicação. Eles não são compilados nem executados e não formam um bundle de
instalação. Nenhuma prova administrativa é distribuída nesta fixture.

| Arquivo | Objeto Git (blob) | SHA-256 |
| --- | --- | --- |
| `VERSION` | `00fee14cde1f9d39dff5b14026831d44fdd0d617` | `70da5a0bdca3a2b0db435e6ad56293f476622514a6e16c5f656c98c2162c183e` |
| `cmd/server/main.go` | `cef4de5d0f15cdecc9a6ad53ca77ae87c68d4eac` | `e00dcd07672e3f89242ffecf489926621020e18e943d3becd5ad8766140db674` |
| `cmd/server/server_runtime.go` | `880fa460211b9fbc8378e002dbce397c12b00438` | `28dac28177886cef83978757d943e013924af575ae4ac58052de0a668e29487c` |
| `cmd/server/legacy_seed_credentials.go` | `22ae8ed50a5c5accc7fad37493278e4a9fd0e334` | `0fc224bc1fa1b27cc339aaaede07f28cdc204ae5a0cc17c8bd3c8fe8589340b5` |
| `internal/security/crypto.go` | `57a9366f050d4aea19984d8d3ee97c9c872f8581` | `e6d72d81fd2d8e4089d718e1027faf214bd5791dff7f14c0eb9d821a6637ab8a` |
