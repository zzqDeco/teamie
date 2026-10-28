# PR #89 reaction picker regression verification

Verified on 2026-10-28 against the PR production frontend build, using local synthetic HTTP/WebSocket fixtures. These checks do not use production accounts or change production data.

## Regression and fix

Opening the last message's reaction picker at 390×844 previously placed the menu at y=778–820 below the message scroller's y=772 edge. All eight emoji buttons were clipped. The picker now renders in a body portal, uses fixed coordinates measured against the visible conversation and viewport, flips above the trigger when needed, and clamps horizontal placement. Scrolling and resizing update its position; scrolling its message out of view dismisses it.

The portal preserves selection, outside-pointer dismissal, Escape/Tab dismissal and keyboard focus/navigation. Existing reaction versioning, API calls and capability checks remain in the message component.

## Automated regressions

`apps/web/src/chat/reaction-picker.test.tsx` adds eight cases covering desktop/mobile first and last messages, left/right bounds, portal placement, scroll/resize updates, mobile visual viewport changes, outside/inside pointer events and keyboard focus restoration. Existing conversation-level reaction tests still exercise add/remove and stale-response handling through the portal.

Run from `apps/web`:

```sh
npm test
npm run lint
npm run build
```

Result: 35 test files / 443 tests passed; lint and production build passed.

## Real browser checks

The built app was exercised in Chrome using the ordinary conversation screen with 40 fixture messages. Each size tested the first and last message:

| Viewport | First message | Last message |
| --- | --- | --- |
| 1440×900 | Pass | Pass |
| 1024×768 | Pass | Pass |
| 768×1024, touch | Pass | Pass |
| 390×844, touch | Pass | Pass |
| 320×568, touch | Pass | Pass |
| 844×390 | Pass | Pass |

For all 12 edge cases, every emoji button center passed pointer hit testing, the whole menu remained inside the visible message area, and opening the picker did not extend the scroller. Clicking an emoji issued the expected PUT and rendered its chip; clicking that chip issued DELETE and removed it. Escape returned focus to the trigger; clicking outside closed the menu without a mutation. No page errors were recorded.

A thirteenth scenario resized an open first-message picker from desktop to mobile, verified all eight buttons remained clickable, exercised arrow-key/Tab behavior, and confirmed scrolling the originating message out of view dismissed the menu.

To repeat manually, open a conversation with enough messages to scroll, set each viewport, select the first/last message's reaction action, and check the interactions above. On mobile first expand the message's action button. For the resize scenario start at the first message so it remains visible after the layout changes.
