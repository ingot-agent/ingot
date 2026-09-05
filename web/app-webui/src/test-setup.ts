import { vi } from 'vitest'

// jsdom has no layout engine; responsive layouts are verified in Playwright.
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener: vi.fn(), removeEventListener: vi.fn(),
    addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
  })),
})
