/**
 * commitlint config — Conventional Commits на русском.
 * См. docs/plans/12-devops.md §2.
 * Запускается из .github/workflows/commitlint.yml (wagoid/commitlint-github-action).
 */
module.exports = {
  extends: ["@commitlint/config-conventional"],
  rules: {
    "type-enum": [
      2,
      "always",
      [
        "feat",
        "fix",
        "perf",
        "refactor",
        "docs",
        "test",
        "build",
        "ci",
        "chore",
        "style",
        "revert",
      ],
    ],
    "type-case": [2, "always", "lower-case"],
    "type-empty": [2, "never"],
    "subject-empty": [2, "never"],
    // описание на русском — запрещаем end-period, но case-правила отключаем,
    // чтобы не ломаться на кириллице
    "subject-case": [0],
    "subject-full-stop": [2, "never", "."],
    "subject-max-length": [2, "always", 100],
    "header-max-length": [2, "always", 120],
    "body-max-line-length": [1, "always", 100],
    "footer-max-line-length": [1, "always", 100],
    "scope-case": [2, "always", "kebab-case"],
    "scope-enum": [
      1, // warning — не жёстко, но подсказка автору
      "always",
      [
        "collector",
        "dedup",
        "render",
        "delivery",
        "grafana",
        "scheduler",
        "state",
        "config",
        "observability",
        "cli",
        "docker",
        "ci",
        "docs",
        "deps",
        "release",
        "test",
      ],
    ],
  },
};
