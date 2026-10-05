// Display names of the cloud providers CostDeck can scale.
export const cloudLabel = (provider?: string): string =>
  provider === 'azure' ? 'Azure' : provider === 'gcp' ? 'Google Cloud' : provider === 'aws' ? 'AWS' : provider || 'Cloud'

// targetLabel shows a cloud resource by name where one is known: Azure identifiers are
// long resource IDs.
export const targetLabel = (identifier: string, targets?: { identifier: string; name?: string }[]): string =>
  targets?.find(t => t.identifier === identifier)?.name || identifier.split('/').pop() || identifier
