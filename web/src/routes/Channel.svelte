<script lang="ts">
  import { onMount } from 'svelte';
  import ChannelHeader from '../components/channels/ChannelHeader.svelte';
  import Announcer from '../components/a11y/Announcer.svelte';
  import Composer from '../components/messages/Composer.svelte';
  import MessageList from '../components/messages/MessageList.svelte';
  import TypingIndicator from '../components/messages/TypingIndicator.svelte';
  import { channelLabel } from '../lib/channels';
  import { t } from '../lib/i18n/en';
  import { channels } from '../lib/state/channels.svelte';
  import { session } from '../lib/state/session.svelte';
  import { users } from '../lib/state/users.svelte';
  import { setFocusedChannel } from '../lib/ws/realtime';

  let { channelId }: { channelId: string } = $props();

  const channel = $derived(channels.byId[channelId]);
  const member = $derived(channelId in channels.memberships);

  // Tell the server which channel is on screen (drives push suppression).
  $effect(() => {
    if (!channel || !member) return;
    setFocusedChannel(channelId);
    return () => setFocusedChannel(null);
  });

  onMount(() => {
    document.querySelector<HTMLElement>('main h1')?.focus();
  });
</script>

{#if channel && member}
  <div class="pane">
    <ChannelHeader {channel} />
    {#key channelId}
      <MessageList {channelId} />
    {/key}
    <TypingIndicator {channelId} />
    <Composer {channelId} channelName={channelLabel(channel, users.byId, session.me?.id ?? '')} />
    <Announcer {channelId} />
  </div>
{:else}
  <div class="missing">
    <h1 tabindex="-1">{t.channel.title}</h1>
    <p>{t.chat.noChannelFound}</p>
    <a href="/">{t.chat.back}</a>
  </div>
{/if}

<style>
  .pane {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .missing {
    padding: var(--s4);
  }
  /* Below the navigation toggle (the shell gives this pane no top padding). */
  @media (max-width: 639px) {
    .missing {
      padding-top: 72px;
    }
  }
</style>
