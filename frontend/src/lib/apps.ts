// Tools registry ("the bench"). Extensible list, mirrors the aboutPanels
// pattern (see components/panels): add a tool by appending to `tools` and the
// landing renders it. Access/status drive each card's open / locked / soon
// state on the landing page; the backend re-enforces access on every request.

export type ToolStatus = 'live' | 'soon';
export type ToolAccess = 'public' | 'friend';
export type ToolAccent = 'mint' | 'sky' | 'citron' | 'coral';

// A Category is a navigation-only grouping in the app menu (see CONTEXT.md).
// It confers no capability and gates nothing — Role decides access.
export type ToolCategory = 'Messaging' | 'Utilities' | 'Experiments';

export interface Tool {
  slug: string;
  name: string;
  /** one plain-language line */
  tagline: string;
  /** navigation grouping for the app menu (Role still decides access) */
  category: ToolCategory;
  /** present only when status === 'live' */
  href?: string;
  status: ToolStatus;
  access: ToolAccess;
  /** drives the card's colour */
  accent: ToolAccent;
}

// Defined display order for the app menu; empty categories are hidden.
export const categoryOrder: ToolCategory[] = [
  'Messaging',
  'Utilities',
  'Experiments',
];

// Menu labels per category (kept separate from the enum values so copy can
// diverge from the type without touching call sites).
export const categoryLabels: Record<ToolCategory, string> = {
  Messaging: 'Messaging',
  Utilities: 'Utilities',
  Experiments: 'Experiments',
};

export const tools: Tool[] = [
  {
    slug: 'whatsapp',
    name: 'WhatsApp Sender',
    tagline:
      'Link your WhatsApp, save templates and lists, send a personalized batch.',
    category: 'Messaging',
    href: '/whatsapp',
    status: 'live',
    access: 'friend',
    accent: 'mint',
  },
  // Two honest placeholders to fill the bench — nothing over-promised. One per
  // remaining category so all three groups show something in the app menu.
  {
    slug: 'soon-1',
    name: 'On the bench',
    tagline: 'Something small is taking shape here.',
    category: 'Utilities',
    status: 'soon',
    access: 'public',
    accent: 'sky',
  },
  {
    slug: 'soon-2',
    name: 'Brewing',
    tagline: 'Another experiment, not ready to show yet.',
    category: 'Experiments',
    status: 'soon',
    access: 'friend',
    accent: 'citron',
  },
];

// Group the tools by category in the defined display order, dropping any empty
// category. Shared by the app menu so the grouping logic lives in one place.
export function toolsByCategory(): { category: ToolCategory; tools: Tool[] }[] {
  return categoryOrder
    .map((category) => ({
      category,
      tools: tools.filter((t) => t.category === category),
    }))
    .filter((group) => group.tools.length > 0);
}
