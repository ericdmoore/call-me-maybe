import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

/**
 * One page per feature. Markdown rather than a data file so the prose is
 * editable by anyone, and the frontmatter carries only what the index and the
 * page chrome need.
 */
const features = defineCollection({
  loader: glob({ pattern: '**/*.md', base: './src/content/features' }),
  schema: z.object({
    title: z.string(),
    // Shown under the title, and on the index card.
    tagline: z.string(),
    // Dialplan code, where the feature has one. Purely informational.
    code: z.string().optional(),
    // Who this is actually for. The index groups nothing by it; it is here to
    // force the question to be answered before a page gets written.
    audience: z.string(),
    order: z.number().default(50),
  }),
});

export const collections = { features };
