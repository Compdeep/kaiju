<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-panel">
      <div class="modal-header">
        <h2>admin</h2>
        <button class="btn-icon" @click="$emit('close')">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
      </div>
      <div class="modal-tabs">
        <button :class="['modal-tab', { active: tab === 'scopes' }]" @click="tab = 'scopes'">scopes</button>
        <button :class="['modal-tab', { active: tab === 'users' }]" @click="tab = 'users'">users</button>
        <button :class="['modal-tab', { active: tab === 'groups' }]" @click="tab = 'groups'">groups</button>
        <button :class="['modal-tab', { active: tab === 'memories' }]" @click="tab = 'memories'">memories</button>
      </div>
      <div class="modal-body">
        <transition name="slide" mode="out-in">
          <ScopesTab v-if="tab === 'scopes'" :key="'scopes'" />
          <UsersTab v-else-if="tab === 'users'" :key="'users'" />
          <GroupsTab v-else-if="tab === 'groups'" :key="'groups'" />
          <MemoriesTab v-else :key="'memories'" />
        </transition>
      </div>
    </div>
  </div>
</template>

<script setup>
/**
 * desc: Admin modal with tabbed navigation for managing scopes, users, groups, and memories
 */
import { ref } from 'vue'
import ScopesTab from './tabs/ScopesTab.vue'
import UsersTab from './tabs/UsersTab.vue'
import GroupsTab from './tabs/GroupsTab.vue'
import MemoriesTab from './tabs/MemoriesTab.vue'

const props = defineProps({ initialTab: { type: String, default: 'scopes' } })
defineEmits(['close'])
const tab = ref(props.initialTab)
</script>
