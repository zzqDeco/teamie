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
  plugins: ["stylelint-declaration-strict-value", noFeaturesOutlineNonePlugin],
  rules: {
    "no-duplicate-selectors": true,
  },
  overrides: [
    {
      files: ["src/styles/**/*.css", "**/src/styles/**/*.css"],
      rules: {
        [noFeaturesOutlineNoneRule]: true,
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
              "z-index": [...cssKeywordValues, "auto", "0", "1", "2", "-1", "-2"],
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
