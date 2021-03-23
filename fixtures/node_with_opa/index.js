// SPDX-FileCopyrightText: 2020 2020 SAP SE or an SAP affiliate company and Cloud Security Client Go contributors
//
// SPDX-License-Identifier: Apache-2.0

setInterval(() => {

    console.log(JSON.stringify(process.env, null, 4));
}, 5000);