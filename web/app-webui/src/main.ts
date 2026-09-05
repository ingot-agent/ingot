import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import ChatView from './views/ChatView.vue'
import OperationsView from './views/OperationsView.vue'
import { i18n } from './i18n'
import './style.css'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/new' },
    { path: '/new', component: ChatView },
    { path: '/sessions/:id', component: ChatView },
    { path: '/operations', component: OperationsView },
    { path: '/:pathMatch(.*)*', redirect: '/new' },
  ],
})
createApp(App).use(createPinia()).use(i18n).use(router).mount('#app')
