import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import jsxA11y from "eslint-plugin-jsx-a11y";

const nonInteractiveInteractionsDefault =
  jsxA11y.flatConfigs.recommended.rules[
    "jsx-a11y/no-noninteractive-element-interactions"
  ]?.[1] ?? {};

const BUTTON_VARIANT_CLASS_SELECTOR_LITERAL =
  'JSXOpeningElement[name.name="Button"] > JSXAttribute[name.name="className"] Literal[value=/\\b(secondary|danger|ghost|primary|small-button|danger-text)\\b/]';
const BUTTON_VARIANT_CLASS_SELECTOR_TEMPLATE =
  'JSXOpeningElement[name.name="Button"] > JSXAttribute[name.name="className"] TemplateElement[value.raw=/\\b(secondary|danger|ghost|primary|small-button|danger-text)\\b/]';
const BUTTON_VARIANT_CLASS_MESSAGE =
  'Pass <Button variant="..." size="..."> instead of variant/size tokens in className.';

const LOCALE_DATE_TIME_SELECTOR =
  'CallExpression[callee.property.name=/^toLocale(Date|Time)String$/]';
const LOCALE_STRING_ZH_SELECTOR =
  'CallExpression[callee.property.name="toLocaleString"][arguments.0.value=/^zh/i]';
const LOCALE_FORMAT_MESSAGE =
  "Use formatDateTime from src/format-datetime.ts instead of direct .toLocale*String date/time formatting.";

const GENERIC_ELEMENT_ARIA_LABEL_SELECTOR =
  'JSXOpeningElement[name.name=/^(span|i|div)$/]:has(JSXAttribute[name.name="aria-label"]):not(:has(JSXAttribute[name.name="role"])):not(:has(JSXSpreadAttribute))';
const GENERIC_ELEMENT_ARIA_LABEL_MESSAGE =
  "Do not place aria-label on generic <span>, <i>, or <div> without a semantic ARIA role; use .sr-only text or an explicit role (e.g., role=\"img\").";

export default tseslint.config(
  { ignores: ["dist", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-hooks/set-state-in-effect": "off",
    },
  },
  {
    files: ["**/*.{tsx,jsx}"],
    ...jsxA11y.flatConfigs.recommended,
    rules: {
      ...jsxA11y.flatConfigs.recommended.rules,
      "jsx-a11y/no-noninteractive-tabindex": [
        "error",
        {
          tags: ["article", "iframe"],
          roles: ["tabpanel", "separator"],
          allowExpressionValues: true,
        },
      ],
      "jsx-a11y/no-noninteractive-element-interactions": [
        "error",
        {
          ...nonInteractiveInteractionsDefault,
          article: [
            "onClick",
            "onMouseDown",
            "onMouseUp",
            "onKeyPress",
            "onKeyDown",
            "onKeyUp",
          ],
        },
      ],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["**/*.test.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: BUTTON_VARIANT_CLASS_SELECTOR_LITERAL,
          message: BUTTON_VARIANT_CLASS_MESSAGE,
        },
        {
          selector: BUTTON_VARIANT_CLASS_SELECTOR_TEMPLATE,
          message: BUTTON_VARIANT_CLASS_MESSAGE,
        },
        {
          selector: GENERIC_ELEMENT_ARIA_LABEL_SELECTOR,
          message: GENERIC_ELEMENT_ARIA_LABEL_MESSAGE,
        },
      ],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["**/*.test.{ts,tsx}", "src/format-datetime.ts"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: BUTTON_VARIANT_CLASS_SELECTOR_LITERAL,
          message: BUTTON_VARIANT_CLASS_MESSAGE,
        },
        {
          selector: BUTTON_VARIANT_CLASS_SELECTOR_TEMPLATE,
          message: BUTTON_VARIANT_CLASS_MESSAGE,
        },
        {
          selector: GENERIC_ELEMENT_ARIA_LABEL_SELECTOR,
          message: GENERIC_ELEMENT_ARIA_LABEL_MESSAGE,
        },
        {
          selector: LOCALE_DATE_TIME_SELECTOR,
          message: LOCALE_FORMAT_MESSAGE,
        },
        {
          selector: LOCALE_STRING_ZH_SELECTOR,
          message: LOCALE_FORMAT_MESSAGE,
        },
      ],
    },
  },
);
