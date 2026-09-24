# Django migration tests not applicable to gormgate

Django 6.0's `tests/migrations/` holds fourteen modules. Eight of them are
ported test-by-test into the matching Go package; each Go test keeps the
Django test name and cites its origin in a
`// django: tests/migrations/<file> <Class>.<test>` comment. This file lists
every test of *those eight* that was **not** ported, with the reason, and
then says what replaces the remaining six, which are not ported test-by-test
at all.

Ported vs. skipped, per suite:

| Django suite | ported | skipped | Go package |
| --- | ---: | ---: | --- |
| `test_optimizer.py` | 42 | 10 | `internal/optimizer` |
| `test_autodetector.py` | 104 | 70 | `internal/autodetector` |
| `test_graph.py` | 18 | 3 | `internal/graph` |
| `test_state.py` | 25 | ~45 | `migrations` |
| `test_loader.py` | 20 | 7 | `internal/loader` |
| `test_questioner.py` | 14 | 0 | `internal/questioner` |
| `test_writer.py` | 42 | 27 | `internal/writer` |
| `test_executor.py` | 22 | 2 | `internal/executor` |

## The six suites that are replaced rather than ported

| Django suite | tests | what replaces it |
| --- | ---: | --- |
| `test_commands.py` | 160 | `itest/parity_test.go`, plus `internal/management`'s unit tests and `itest/e2e_test.go` |
| `test_operations.py` | 174 | `itest/internal/conformance` (the per-vendor schema cases), `migrations/state_test.go` and `internal/executor` |
| `test_multidb.py` | 10 | `itest/api_test.go` (`TestRouters`) and the `--database` steps of the parity scenarios |
| `test_exceptions.py` | 1 | nothing: it asserts the `repr()` of Django's `NodeNotFoundError`, a Python-only shape |
| `test_deprecated_fields.py` | 1 | nothing: it checks that Django's deprecated field classes still work in historic migrations; gormgate has no deprecated fields |
| `test_base.py` | 0 | nothing: it is `MigrationTestBase`, the helper the other suites subclass, not a suite of its own |

### `test_commands.py` — the delta

This is the largest upstream suite and the only one that covers the commands
themselves, so the substitution deserves to be spelled out. `test_commands.py`
calls the commands in-process with `call_command` and asserts on captured
output; `itest/parity_test.go` instead runs each command line against a real
Django 6.0 project in Docker **and** against an equivalent gormgate project,
and requires the two to agree on what they did: the same exit status, the
same migrations written, the same operations planned. Where upstream
asserts that Django takes a particular decision, the parity harness asserts
that gormgate takes whatever decision Django actually takes, which is the
stronger claim.

The suites are not the same size. Upstream runs 160 test methods; the parity
harness runs 15 scenarios of 122 command invocations, and covers:

- `makemigrations` (including `--dry-run`, `--merge`, `--empty`, `--name`,
  `--check`, `--scriptable`, `--no-header`, `--update`, `--no-input` and the
  interactive questioner),
- `migrate` (including `--plan`, `--check`, `--fake`, `--fake-initial`,
  `--run-syncdb`, `--prune` and its squashed-replacements branch, and
  `<app> zero`),
- `showmigrations` (the list and `--plan`, at two verbosities),
- `sqlmigrate` (forwards, `--backwards`, the empty-migration branch, and the
  transaction framing),
- `squashmigrations` (the prompt loop, `--no-input`, `--squashed-name`,
  `--no-optimize`, the start-migration form and the name-taken error),
- `optimizemigration` (`--check` and the rewrite),
- `check` (the clean run and its footer on stdout, `--fail-level`, app
  labels, `--tag`, an unknown tag and an unknown app label),
- `sqlsequencereset` (the SQL itself, for one app, for several apps and in
  both argument orders, plus its argument errors),
- the global options `--no-color`, `--force-color`, `--skip-checks`, `-q`
  and `-v`.

Help and usage text is not compared at all: it is cobra's, not Django's,
and the mapping between the two command lines is in
[Feature map](from-django.md).

What it does not cover, and upstream does: `loaddata`/`dumpdata` and the
other commands gormgate does not have; the `MIGRATION_MODULES` matrix beyond
the one disabled app of the `run_syncdb_unmigrated_app` scenario; migrations
in a namespace package; and the many upstream cases that vary
`INSTALLED_APPS` per test, which the harness cannot do without rebuilding
both projects.

`inspectdb`'s **output** is the one thing the harness cannot compare, and it
is not a question of configuration: the command prints Python model classes
on one side and Go structs with gorm tags on the other, so there is no
rendering of the two that is meaningfully the same text. Its argument errors
are compared; what it produces is covered instead by `itest/e2e_test.go`,
which runs it against a real schema, asserts the models it names and
**compiles** the result, and by `TestConformance`, which compares
introspection against the expected state for every operation on every
database.

Both projects run on the same PostgreSQL server, which is what makes
`sqlsequencereset` comparable at all — the SQL it prints has nothing
Python or Go in it. Adding that comparison immediately found a real
difference: gormgate listed an app's models in alphabetical order where
Django lists them in the order they are declared. It now follows the order
the models were passed to `gormgate.App`, in `sqlsequencereset` and in
`migrate --run-syncdb`, which is the same place Django's `get_models()`
order comes from.

The reasons fall into a small number of groups, and the group is named on every
entry:

- **managers** — gormgate has no model managers.
- **proxy / MTI / bases** — gormgate has no model inheritance. Django's
  *abstract* base class does have a counterpart and is supported: it is a
  gorm embedded struct, and its fields are migrated like any others. A
  proxy model can be approximated by a second type over the same table,
  marked unmanaged. Multi-table inheritance has no counterpart, because
  gorm has no parent model for the implicit key to point at. See
  [Feature map](from-django.md).
- **order_with_respect_to** — not implemented.
- **swappable** — no `AUTH_USER_MODEL` / swappable models / settings.
- **index_together** — removed in favour of `Meta.Indexes`.
- **contenttypes** — no contenttypes app.
- **m2m field** — gorm `many2many` join tables are auto-created models, not a
  field type (see `docs/deviations.md`, section 4).
- **db_column** — a field's name *is* its column (see `docs/deviations.md`,
  section 3).
- **Python-only** — the test exercises Python semantics with no Go counterpart.

---

## test_autodetector.py — 70 skipped

### Proxy models (8) — *proxy*
`test_proxy`, `test_proxy_non_model_parent`, `test_proxy_custom_pk`,
`test_proxy_fk_dependency`, `test_proxy_to_mti_with_fk_to_proxy`,
`test_proxy_to_mti_with_fk_to_proxy_proxy`, `test_proxy_bases_first`,
`test_alter_model_options_proxy`.

### MTI / model bases (5) — *MTI/bases*
`test_bases_first`, `test_bases_first_mixed_case_app_label`,
`test_multiple_bases`, `test_mti_inheritance_model_removal`,
`test_add_model_with_field_removed_from_base_model`.

### order_with_respect_to (8) — *order_with_respect_to*
`test_set_alter_order_with_respect_to`, `test_add_alter_order_with_respect_to`,
`test_remove_alter_order_with_respect_to`,
`test_add_model_order_with_respect_to`,
`test_add_model_order_with_respect_to_unique_together`,
`test_add_model_order_with_respect_to_constraint`,
`test_add_model_order_with_respect_to_index`,
`test_set_alter_order_with_respect_to_index_constraint_unique_together`.

### swappable / AUTH_USER_MODEL (10) — *swappable*
`test_swappable`, `test_swappable_lowercase`, `test_swappable_changed`,
`test_swappable_many_to_many_model_case`, `test_swappable_first_inheritance`,
`test_swappable_first_setting`, `test_swappable_circular_multi_mti`,
`test_circular_dependency_swappable`, `test_circular_dependency_swappable2`,
`test_circular_dependency_swappable_self`.

### ManyToManyField / through models (11) — *m2m field*
`test_alter_many_to_many`, `test_create_with_through_model`,
`test_create_with_through_model_separate_apps`,
`test_many_to_many_removed_before_through_model`,
`test_many_to_many_removed_before_through_model_2`,
`test_m2m_w_through_multistep_remove`,
`test_concrete_field_changed_to_many_to_many`,
`test_many_to_many_changed_to_concrete_field`,
`test_alter_unique_together_fk_to_m2m`, `test_rename_m2m_through_model`,
`test_renamed_referenced_m2m_model_case`.
Partially replaced by `TestAutodetector_AddManyToMany`, which asserts that
adding a gorm `many2many` produces a single `CreateModel` for the join model
with both foreign keys inside it.

### GeneratedField (5) — *not implemented*
`test_add_field_before_generated_field`, `test_add_fk_before_generated_field`,
`test_remove_generated_field_before_its_base_field`,
`test_remove_generated_field_before_multiple_base_fields`,
`test_remove_generated_field_and_one_of_multiple_base_fields`.
gormgate has no generated/computed column operation.

### db_column (4) — *db_column*
`test_rename_field_preserved_db_column`,
`test_rename_related_field_preserved_db_column`,
`test_rename_field_preserve_db_column_preserve_constraint`,
`test_rename_field_preserve_db_column_recreate_constraint`.
`test_rename_field_without_db_column_recreate_constraint` *is* ported — in
gormgate it is always that path.

### ForeignObject / multi-column FKs (2) — *not implemented*
`test_foreign_object_from_to_fields_list`, `test_rename_foreign_object_fields`.
`m.ForeignKey` references a single column.

### CompositePrimaryKey field (2) — *not implemented*
`test_add_composite_pk`, `test_remove_composite_pk`. A composite primary key in
gormgate is several fields with `PrimaryKey: true`, not a field object.

### Python-only deconstruction (7) — *Python-only*
`test_supports_functools_partial`, `test_deconstruct_field_kwarg`,
`test_deconstructible_list`, `test_deconstructible_tuple`,
`test_deconstructible_dict`, `test_nested_deconstructible_objects`,
`test_add_constraints_with_dict_keys`.
Partially replaced by `TestAutodetector_CustomDeconstructible` and
`TestAutodetector_DeconstructType`, which pin gormgate's rule that function
defaults compare by fully-qualified name.

### Validators (3) — *Python-only*
`test_identical_regex_doesnt_alter`, `test_different_regex_does_alter`,
`test_alter_regex_string_to_compiled_regex`. Fields carry no validators.

### Other (5)
- `test_alter_model_managers` — *managers*.
- `test_alter_model_options` — Django's options dict; gormgate's
  `AlterModelOptions` carries only `Managed`, covered by the managed-transition
  tests.
- `test_default_related_name_option` — no `related_name`.
- `test_add_blank_textfield_and_charfield` — no `blank` (see
  `deviations.md`, section 7: a non-null string field always prompts for a
  default).
- `test_add_custom_fk_with_hardcoded_to` — needs a `ForeignKey` subclass that
  overrides `deconstruct()`.

`test_new_model`'s manager assertion is dropped; its model-creation half is
ported.

---

## test_optimizer.py — 10 skipped

- `test_none_app_label` — *Python-only*: asserts `TypeError` when `app_label`
  is not a `str`; `Optimize(ops []m.Operation, app string)` enforces it at
  compile time.
- `test_create_alter_model_managers` — *managers*.
- `test_create_alter_index_delete_model`, `test_alter_alter_index_model`,
  `test_create_alter_index_field` — *index_together* (`AlterIndexTogether`).
- `test_create_alter_owrt_delete_model`, `test_alter_alter_owrt_model`,
  `test_create_alter_owrt_field` — *order_with_respect_to*.
- `test_create_model_no_reordering_of_inherited_model` — *MTI/bases*.
- `test_create_model_add_field_not_through_m2m_through` — *m2m field*.

Partial skips inside ported tests, noted in the test comments: the `bases=(...)`
assertions of `test_optimize_through_create`; the "narrow two options down to
one" assertion of `test_create_model_and_remove_model_options` (only `Managed`
exists); the two `old_fields=` assertions of `test_rename_index` (gormgate
renames indexes by name only).

---

## test_graph.py — 3 skipped

- `NodeTests.test_node_repr`, `test_dummynode_repr`, and the `repr(graph)` half
  of `test_stringify` — *Python-only*: no `repr()`, and gormgate has no separate
  `DummyNode` type (it is a flag on `Node`). `str(node)` is ported via
  `graph.Repr`.

---

## test_state.py — skipped

- `test_create`, `test_ignore_order_wrt`, `test_composite_pk_state`,
  `test_choices_iterator`, `test_explicit_index_name`,
  `test_from_model_constraints`, `test_abstract_model_children_inherit_indexes`,
  `test_custom_model_base` — all exercise `ModelState.from_model`, which
  gormgate replaces with `internal/fromgorm` (tested in its own package).
- `test_custom_default_manager*`, `test_no_duplicate_managers`,
  `test_custom_base_manager`, `test_manager_refer_correct_model_version`,
  `test_custom_manager_swappable` — *managers*.
- `test_render`, `test_render_model_inheritance`,
  `test_render_model_with_multiple_inheritance`,
  `test_render_project_dependencies` — *MTI/bases* (`InvalidBasesError`).
- `test_create_swappable`, `test_create_swappable_from_abstract` — *swappable*.
- `test_order_with_respect_to_private_field`,
  `test_modelstate_get_field_order_wrt`,
  `test_modelstate_get_field_no_order_wrt_order_field`,
  `test_get_order_field_after_removed_order_with_respect_to_field` —
  *order_with_respect_to*.
- `test_apps_bulk_update` — `StateApps.bulk_update()` / `apps.ready`; gormgate's
  `Apps` has no staged-update mode.
- `test_reload_related_model_on_non_relational_fields`,
  `test_reload_model_relationship_consistency`,
  `StateRelationsTests.test_relations_population` — apps-registry reload
  internals and the lazy `_relations` cache. gormgate invalidates the whole
  rendered `Apps` on every mutation and computes references on demand; the
  observable consequences are covered by `TestState_SelfRelation`,
  `TestState_AddRelations` and `TestState_RemoveRelations`.
- `test_real_apps_non_set` — *Python-only* (`assert isinstance(real_apps, set)`).
- `ModelStateTests.test_repr` — *Python-only*.
- `test_bound_field_sanity_check`, `test_sanity_check_to`,
  `test_sanity_check_through` — *Python-only*: reject bound field instances and
  non-string `to`/`through`; unrepresentable in Go's type system.
- `StateRelationsTests.test_add_field_m2m_with_through` — *m2m field*.
- all of `RelatedModelsTests` (~20) — tests `get_related_models_recursive`, used
  only by the reload machinery; the cases are proxy/abstract/MTI/generic-FK
  based.

---

## test_loader.py — 7 skipped

- `RecorderTests.test_apply`, `RecorderTests.test_has_table_cached` — need a
  database; the recorder is exercised end to end through the fake backend in
  `internal/executor` and for real in `itest/`.
- `LoaderTests.test_load_module_file`, `test_loading_namespace_package`,
  `test_loading_package_without__file__`, `PycLoaderTests.test_valid`,
  `PycLoaderTests.test_invalid` — *Python-only* import machinery (a module that
  is a file, namespace packages, frozen modules, stale `.pyc`). gormgate has no
  import step; the equivalent failure modes are covered by
  `TestLoader_LoadEmptyDir`, `TestLoader_LoadNotCompiled` and
  `TestLoader_DiskConsistency`.

`test_load_import_error` and `test_explicit_missing_module` are **adapted**
rather than skipped: the `ImportError` becomes the "not compiled into this
command" command error.

---

## test_questioner.py — 0 skipped

All 14 tests are ported. `_ask_default` prompts for a *Go* expression
type-checked with `go/types` instead of `eval()`ing Python — see
`docs/deviations.md`, section 7.

---

## test_writer.py — 27 skipped, all *Python-only*

- Python value types with no migration-state counterpart: `test_serialize_uuid`,
  `test_serialize_pathlib`, `test_serialize_path_like`, `test_serialize_range`,
  `test_serialize_builtins`, `test_serialize_builtin_types`,
  `test_serialize_type_model`.
- `choices` / flag enums: `test_serialize_choices`,
  `test_serialize_dictionary_choices`, `test_serialize_callable_choices`,
  `test_serialize_enum_flags`.
- Partial application: `test_serialize_functools_partial`,
  `test_serialize_functools_partial_posarg`,
  `test_serialize_functools_partial_kwarg`,
  `test_serialize_functools_partial_mixed`,
  `test_serialize_functools_partial_non_identifier_keyword`,
  `test_serialize_functools_partialmethod`.
- Value types absent from migration state: `test_serialize_set`,
  `test_serialize_frozensets`, `test_serialize_iterators`,
  `test_serialize_lazy_objects`, `test_serialize_compiled_regex`.
- `test_serialize_managers` — *managers*.
- `test_serialize_settings` — *swappable*/settings.
- `test_serialize_non_identifier_keyword_args` — Go struct fields are always
  identifiers.
- `test_register_serializer` (only its error half is ported) and
  `test_register_non_serializer` — there is no serializer registry.
- `test_migration_path_distributed_namespace` — namespace packages.

---

## test_executor.py — 2 skipped

- `test_custom_user` — *swappable* (`model._meta.swapped`).
- `test_alter_id_type_with_fk` — pure DDL integration (altering a primary-key
  type and the foreign-key columns referencing it). Real schema-editor
  behaviour; it belongs in `itest/`.

`test_detect_soft_applied_add_field_manytomanyfield` is **adapted**: Django's
version turns on the implicit through table of a `ManyToManyField` added by
`AddField`. gormgate has no implicit through tables, so the Go test follows
Django's exact call sequence but exercises the *column* branch of
`detect_soft_applied`.
