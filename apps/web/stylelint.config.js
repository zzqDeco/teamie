import stylelint from "stylelint";

const noFeaturesOutlineNoneRule = "teamie/no-features-outline-none";
const noFeaturesOutlineNoneMessages = stylelint.utils.ruleMessages(noFeaturesOutlineNoneRule, {
  rejected:
    "Do not strip focus outlines inside @layer features; move custom wrapper focus controls to @layer components with an explicit :focus-visible or :focus-within ring.",
});

const noFeaturesOutlineNonePlugin = stylelint.createPlugin(
  noFeaturesOutlineNoneRule,
  (primary) => (root, result) => {
    if (!primary) return;
    root.walkAtRules("layer", (layerRule) => {
      if (layerRule.params.trim() !== "features") return;
      layerRule.walkDecls(/^outline(-style)?$/, (decl) => {
        if (/^(none|0)\b/i.test(decl.value.trim())) {
          stylelint.utils.report({
            ruleName: noFeaturesOutlineNoneRule,
            result,
            node: decl,
            message: noFeaturesOutlineNoneMessages.rejected,
          });
        }
      });
    });
  },
);

const strictTransitionDurationRule = "teamie/strict-transition-duration";
const strictTransitionDurationMessages = stylelint.utils.ruleMessages(
  strictTransitionDurationRule,
  {
    rejected: (raw) =>
      `Unexpected raw transition duration "${raw}"; use var(--dur-fast | --dur-base | --dur-slow) instead.`,
  },
);

const strictTransitionDurationPlugin = stylelint.createPlugin(
  strictTransitionDurationRule,
  (primary) => (root, result) => {
    if (!primary) return;
    root.walkDecls(/^transition$/, (decl) => {
      const value = decl.value.trim();
      if (value === "none" || value === "inherit" || value === "initial" || value === "unset") {
        return;
      }
      // Strip var(...) calls before scanning for raw non-zero <time> literals.
      const stripped = value.replace(/var\([^)]*\)/g, "");
      for (const match of stripped.matchAll(/(?<![a-zA-Z0-9_-])(\d*\.?\d+)(ms|s)\b/gi)) {
        const numeric = Number(match[1]);
        if (numeric === 0) continue;
        stylelint.utils.report({
          ruleName: strictTransitionDurationRule,
          result,
          node: decl,
          message: strictTransitionDurationMessages.rejected(match[0]),
        });
      }
    });
  },
);

const cssKeywordValues = ["inherit", "initial", "unset", "revert", "revert-layer"];
const spacingAllowedValues = [
  ...cssKeywordValues,
  "0",
  "auto",
  "1px",
  "-1px",
  "/^calc\\(/",
  "/^max\\(/",
  "/^min\\(/",
  "/^clamp\\(/",
  "/^env\\(/",
  "/%$/",
];

/** @type {import("stylelint").Config} */
export default {
  plugins: [
    "stylelint-declaration-strict-value",
    noFeaturesOutlineNonePlugin,
    strictTransitionDurationPlugin,
  ],
  rules: {
    "no-duplicate-selectors": true,
  },
  overrides: [
    {
      files: ["src/styles/**/*.css", "**/src/styles/**/*.css"],
      rules: {
        [noFeaturesOutlineNoneRule]: true,
        [strictTransitionDurationRule]: true,
        "scale-unlimited/declaration-strict-value": [
          [
            "color",
            "background-color",
            "border-color",
            "border-top-color",
            "border-right-color",
            "border-bottom-color",
            "border-left-color",
            "outline-color",
            "fill",
            "stroke",
            "font-size",
            "line-height",
            "transition-duration",
            "z-index",
            "/^(row-|column-)?gap$/",
            "/^padding(-(top|right|bottom|left|inline|block)(-start|-end)?)?$/",
            "/^margin(-(top|right|bottom|left|inline|block)(-start|-end)?)?$/",
          ],
          {
            disableFix: true,
            ignoreVariables: true,
            ignoreFunctions: false,
            ignoreValues: {
              "": [...cssKeywordValues, "transparent", "currentColor", "none"],
              "font-size": [...cssKeywordValues, "/^clamp\\(/", "/%$/", "/em$/"],
              "line-height": [...cssKeywordValues, "normal", "/^calc\\(/"],
              "transition-duration": [...cssKeywordValues, "0s", "0ms"],
              "z-index": [...cssKeywordValues, "auto", "0", "1", "-1", "-2"],
              "/^(row-|column-)?gap$/": spacingAllowedValues,
              "/^padding(-(top|right|bottom|left|inline|block)(-start|-end)?)?$/":
                spacingAllowedValues,
              "/^margin(-(top|right|bottom|left|inline|block)(-start|-end)?)?$/":
                spacingAllowedValues,
            },
          },
        ],
      },
    },
  ],
};
