/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package command

import (
	"flag"
	"fmt"
	"strconv"
)

func DecimalFlag(flags *flag.FlagSet, value *uint64, name string, initial uint64, description string) {
	*value = initial
	flags.Func(name, description, func(text string) error {
		if text == "" {
			return fmt.Errorf("%s requires a decimal integer", name)
		}
		for _, c := range text {
			if c < '0' || c > '9' {
				return fmt.Errorf("%s requires a decimal integer", name)
			}
		}
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return err
		}
		*value = n
		return nil
	})
}
