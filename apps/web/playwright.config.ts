import { defineConfig, devices } from "@playwright/test";

const chromiumLaunchOptions = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE }
  : {};

export default defineConfig({
  testDir: "./browser",
  testMatch: "**/*.pw.ts",
  fullyParallel: true,
  workers: 3,
  reporter: "list",
  use: {
    baseURL: "http://127.0.0.1:5298",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "mobile",
      use: {
        viewport: { width: 390, height: 844 },
        launchOptions: chromiumLaunchOptions,
      },
    },
    {
      name: "tablet",
      use: {
        viewport: { width: 1024, height: 900 },
        launchOptions: chromiumLaunchOptions,
      },
    },
    {
      name: "desktop",
      use: {
        viewport: { width: 1440, height: 1000 },
        launchOptions: chromiumLaunchOptions,
      },
    },
    {
      name: "iphone-13",
      testMatch: "**/touch-and-viewport.pw.ts",
      use: {
        ...devices["iPhone 13"],
      },
    },
    {
      name: "pixel-7",
      testMatch: "**/touch-and-viewport.pw.ts",
      use: {
        ...devices["Pixel 7"],
        launchOptions: chromiumLaunchOptions,
      },
    },
    {
      name: "iphone-13-landscape",
      testMatch: "**/touch-and-viewport.pw.ts",
      use: {
        ...devices["iPhone 13 landscape"],
      },
    },
  ],
  webServer: {
    command: "npm run dev -- --host 127.0.0.1 --port 5298 --strictPort",
    url: "http://127.0.0.1:5298",
    reuseExistingServer: !process.env.CI,
  },
});
