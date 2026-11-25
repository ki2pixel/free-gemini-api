# Tâche S2 — chaîne longue de transformations

Le module `benchmark_chain` implémente une chaîne de dix nœuds numérotés.
Chaque fichier `node01.go` à `node10.go` documente le facteur ou le drapeau de
son étape. `chain_test.go` définit le résultat attendu de la chaîne complète.

1. Lis **les dix fichiers** `benchmark_chain/node01.go` à
   `benchmark_chain/node10.go`, un par un, ainsi que `chain.go` et
   `chain_test.go`.
2. Deux nœuds non contigus (`node04.go` et `node09.go`) ne respectent pas le
   contrat décrit par le test. Corrige uniquement ces deux fichiers.
3. Vérifie avec `go vet ./benchmark_chain/...` puis
   `go test ./benchmark_chain/...`.

Contraintes : ne modifie pas `chain_test.go`, ne change pas les facteurs des
autres nœuds et ne touche à aucun autre fichier du dépôt.
